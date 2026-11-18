package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openfox/internal/runtime"
)

const outputArchiveLimit = 8 * 1024 * 1024
const archiveFooter = "\n\n[OpenFox: archive limit reached; later output was not retained.]\n"

// One private, bounded text file per Run. It is never exposed as a writable
// Runtime directory. Only decoded text selected before preview clipping enters
// this archive; native images, protocol metadata and arguments do not.
// record and finish are called serially, with finish after Execution.Wait.
type outputArchive struct {
	root              string
	redact            func(string) string
	dir               string
	file              *os.File
	bytes, replyBytes int
	required, capped  bool
	err               error
}

func (a *outputArchive) record(kind, id, text string) {
	if text == "" {
		return
	}
	if a.redact != nil {
		text = a.redact(text)
		id = a.redact(id)
	}
	if kind == "reply" {
		if a.replyBytes <= maxResultOutput {
			// Saturate: an arbitrarily long stream does not grow this counter.
			a.replyBytes += min(len(text)+1, maxResultOutput+1)
		}
		a.required = a.required || a.replyBytes > maxResultOutput
	} else {
		a.required = true
	}
	if a.err != nil || a.capped {
		return
	}
	if a.file == nil {
		if a.err = os.MkdirAll(a.root, 0700); a.err != nil {
			return
		}
		removeStaleAttachmentDirs(a.root, time.Now())
		a.dir, a.err = os.MkdirTemp(a.root, "run-")
		if a.err != nil {
			return
		}
		a.file, a.err = os.OpenFile(filepath.Join(a.dir, "output.txt"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if a.err != nil {
			return
		}
	}
	header := fmt.Sprintf("\n--- %s %s ---\n", boundUTF8(kind, 64), boundUTF8(id, 128))
	text = header + text + "\n"
	remaining := outputArchiveLimit - len(archiveFooter) - a.bytes
	if len(text) > remaining {
		text = boundUTF8(text, remaining) + archiveFooter
		a.capped = true
	}
	var n int
	n, a.err = io.WriteString(a.file, text)
	a.bytes += n
	if a.err == nil && n != len(text) {
		a.err = io.ErrShortWrite
	}
}

func (a *outputArchive) close() {
	if a.file != nil {
		_ = a.file.Close()
	}
	if a.dir != "" {
		_ = os.RemoveAll(a.dir)
	}
}

func (a *outputArchive) content() ([]byte, error) {
	if a.err != nil {
		return nil, a.err
	}
	if a.file == nil {
		return nil, fmt.Errorf("output archive unavailable")
	}
	if _, err := a.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// Read from the held descriptor, never reopen a model-controlled path.
	return io.ReadAll(io.LimitReader(a.file, outputArchiveLimit))
}

func (r *runState) attachOutputArchive(ctx context.Context, archive *outputArchive) {
	if !archive.required {
		return
	}
	warn := func(text string) { r.events.add(runtime.Event{Type: runtime.EventWarning, Text: text}) }
	content, err := archive.content()
	if err != nil {
		warn("输出预览已截断；执行输出文件保存失败，未提供完整下载。任务状态不受影响。")
		return
	}
	if r.artifacts == nil {
		r.artifacts = newArtifactPublisher(nil, "", r.uploadArtifact)
	}
	name := "执行输出.txt"
	if archive.capped {
		name = "执行输出（部分）.txt"
	}
	sum := sha256.Sum256(content)
	file := artifactFile{Name: name, MIME: "text/plain", Content: content, SHA256: hex.EncodeToString(sum[:])}
	uploadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resultDeadline)
	defer cancel()
	_, _, err = r.artifacts.publishFile(uploadCtx, name, file)
	if err != nil {
		// Keep real execution success/failure separate from optional log delivery.
		warn("输出预览已截断；执行输出文件未能上传，可能受到网络或组织产物配额限制。任务状态不受影响。")
		return
	}
	if archive.capped {
		warn("执行输出文件已达到 8 MiB，仅保留部分文本；后续执行继续完成。可在本次产物中下载已保留内容。")
	} else if strings.Contains(string(content), runtime.OutputTruncationNotice) {
		warn("执行输出文件包含已截断的文本，不能视为完整日志。可在本次产物中下载已保留内容。")
	}
}
