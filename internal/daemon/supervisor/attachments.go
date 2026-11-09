package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"openfox/internal/runtime"
)

// 把对话消息附件下载到 daemon 自己的每个 Run 临时目录，并在 prompt 开头列出绝对路径。
// 目录在工作目录之外（DIRECT 隔离会直接使用用户的真实仓库，不能往里写文件，Agent 也不会把附件提交进去）：
//
//	<AttachmentRoot>/<runID>/<附件 ID>/<清洗后的文件名>
//
// Run 结束后整个 <runID> 目录删除；daemon 异常退出遗留的目录在之后的 Run 准备时按过期时间清理。
// Runtime 只需要读取：Codex 的 workspace-write 沙箱允许读取工作目录之外的文件，所以不扩大可写路径。

const (
	// maxRunAttachments 与中心一致；maxAttachmentBytes 是单个附件的绝对上限（中心上限 20 MB）。
	maxRunAttachments  = 30
	maxAttachmentBytes = 20 << 20
	maxAttachmentName  = 100
	// staleAttachmentsAfter 与独立 CODEX_HOME 的过期时间一致。
	staleAttachmentsAfter = runtime.MaxRunTimeout + time.Hour
)

// safeAttachmentName 清洗文件名：只保留最后一个路径成分，控制字符与路径分隔符换成下划线，限制长度并保留扩展名。
func safeAttachmentName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	if len(name) > maxAttachmentName {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		stem := name[:maxAttachmentName-len(ext)]
		for !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
		name = stem + ext
	}
	return name
}

func (s *Supervisor) attachmentRoot() string {
	if root := strings.TrimSpace(s.config.AttachmentRoot); root != "" {
		return root
	}
	return filepath.Join(os.TempDir(), "openfox-attachments")
}

// removeStaleAttachmentDirs 删除修改时间早于 staleAttachmentsAfter 的 Run 目录（异常退出遗留）。
func removeStaleAttachmentDirs(root string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && entry.IsDir() && now.Sub(info.ModTime()) > staleAttachmentsAfter {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
}

// fetchAttachments 逐个下载附件并返回要加在 prompt 前面的说明与清理函数（总是可调用，没有附件时为空操作）。
// 任何一个下载失败即返回错误并先清理已下载的文件（调用方走 prepare-failed）。
func (r *runState) fetchAttachments(ctx context.Context, runContext RunContext) (string, func(), error) {
	noop := func() {}
	if len(runContext.Attachments) == 0 {
		return "", noop, nil
	}
	if len(runContext.Attachments) > maxRunAttachments {
		return "", noop, errors.New("too many attachments")
	}
	// Run ID 来自中心，仍按路径成分校验，避免越出下载根目录。
	runID := r.offer.RunID
	if runID == "" || strings.ContainsAny(runID, `/\.`) {
		return "", noop, errors.New("invalid run id")
	}
	root := r.supervisor.attachmentRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", noop, fmt.Errorf("create attachment root: %w", err)
	}
	removeStaleAttachmentDirs(root, time.Now())
	runDir := filepath.Join(root, runID)
	cleanup := func() { _ = os.RemoveAll(runDir) }
	client := r.supervisor.config.Client
	var lines []string
	for _, entry := range runContext.Attachments {
		if entry.ID == "" || strings.ContainsAny(entry.ID, `/\.`) || entry.SizeBytes < 0 || entry.SizeBytes > maxAttachmentBytes {
			cleanup()
			return "", noop, errors.New("invalid attachment manifest entry")
		}
		dir := filepath.Join(runDir, entry.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			cleanup()
			return "", noop, fmt.Errorf("create attachment directory: %w", err)
		}
		target := filepath.Join(dir, safeAttachmentName(entry.Name))
		if err := r.downloadAttachment(ctx, client, entry, dir, target); err != nil {
			cleanup()
			return "", noop, err
		}
		lines = append(lines, fmt.Sprintf("- %s（%s，%d 字节）", target, entry.ContentType, entry.SizeBytes))
	}
	note := "用户随消息发送了以下附件，已保存在本机（只需读取，不要修改或提交到仓库）：\n" + strings.Join(lines, "\n") + "\n\n"
	return note, cleanup, nil
}

func (r *runState) downloadAttachment(ctx context.Context, client *Client, entry RunAttachment, dir, target string) error {
	temporary, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return fmt.Errorf("create attachment file: %w", err)
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if err := client.DownloadRunAttachment(ctx, r.offer.OrganizationID, r.offer.RunID, entry.ID, r.offer.Generation, temporary, entry.SizeBytes); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("download attachment %q: %s", entry.Name, downloadErrorText(err))
	}
	info, err := temporary.Stat()
	if err != nil || info.Size() != entry.SizeBytes {
		_ = temporary.Close()
		return fmt.Errorf("attachment %q size mismatch", entry.Name)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporary.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), target)
}
