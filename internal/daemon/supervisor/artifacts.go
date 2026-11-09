package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"openfox/internal/runtime"
)

// 会话产物：Agent 用发布工具 openfox_publish_artifact 主动交付文件；回复里写本地路径不算交付。
// 工具在电脑端本地执行：解析路径、校验、读取、上传到中心，不把文件读取转发给中心。
//
// 允许目录（符号链接解析后必须仍在其中之一）：
//   - 本次 Run 的工作目录；
//   - 电脑端为本次 Run 新建的专属产物目录 <ArtifactRoot>/<runID>（工作目录之外，解决沙箱不让写工作目录的问题，
//     路径通过提示词告诉 Agent；Run 结束后删除，异常退出遗留的目录在之后的 Run 准备时按过期时间清理）。
//
// 校验（纯函数，见 inspectArtifact）：路径越界、敏感文件、非普通文件（目录、设备、管道、套接字）、超大小一律拒绝；
// 扩展名白名单 + 内容嗅探决定 MIME，两者不符时按更保守的一方（只能下载）。每次 Run 最多 maxRunArtifacts 个，
// 按 sha256 去重（重复发布同一内容返回已有产物）。

const (
	publishToolName = toolPrefix + "publish_artifact"
	// maxRunArtifacts 与中心 executiondelivery.MaxRunArtifacts 一致；maxArtifactFileBytes 是单个产物的绝对上限（中心另按组织策略再校验）。
	maxRunArtifacts      = 10
	maxArtifactFileBytes = 20 << 20
	maxArtifactPathBytes = 4096
	maxArtifactTitle     = 100
	octetStream          = "application/octet-stream"
)

// artifactRejection 是可以原样告诉 Agent 的拒绝原因（不含本机以外的信息）。
type artifactRejection struct{ reason string }

func (e *artifactRejection) Error() string { return e.reason }

func reject(format string, args ...any) error {
	return &artifactRejection{reason: fmt.Sprintf(format, args...)}
}

// artifactFile 是通过校验、已读入内存的文件。
type artifactFile struct {
	// Path 是解析符号链接后的真实路径（只用于本机日志与测试，不上报中心）。
	Path    string
	Name    string
	MIME    string
	Content []byte
	SHA256  string
}

// sensitiveDirs 是路径里任何一段命中即拒绝的目录名（小写比较）。
var sensitiveDirs = map[string]struct{}{
	".git": {}, ".ssh": {}, ".gnupg": {}, ".aws": {}, ".kube": {}, ".docker": {},
}

// sensitiveNames 是整名命中即拒绝的文件名（小写比较）。
var sensitiveNames = map[string]struct{}{
	".netrc": {}, ".npmrc": {}, ".pypirc": {}, ".pgpass": {}, ".git-credentials": {},
}

// sensitiveExts 是扩展名命中即拒绝的文件（小写比较，含点）。
var sensitiveExts = map[string]struct{}{
	".pem": {}, ".key": {}, ".p12": {}, ".pfx": {}, ".keystore": {}, ".jks": {},
}

// sensitiveName 判断单个文件名是否是敏感文件：.env*、id_rsa* 等私钥、证书与凭据文件。
func sensitiveName(name string) bool {
	lower := strings.ToLower(name)
	if lower == ".env" || strings.HasPrefix(lower, ".env.") {
		return true
	}
	for _, prefix := range []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if _, ok := sensitiveNames[lower]; ok {
		return true
	}
	_, ok := sensitiveExts[filepath.Ext(lower)]
	return ok
}

// sensitivePath 判断相对路径（相对允许目录）里是否有敏感目录或敏感文件名。
func sensitivePath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for index, part := range parts {
		if _, ok := sensitiveDirs[strings.ToLower(part)]; ok && index < len(parts)-1 {
			return true
		}
		if part == ".git" || part == ".ssh" {
			return true
		}
	}
	return sensitiveName(parts[len(parts)-1])
}

// textExts 是按文本预览的扩展名 → MIME。SVG、HTML 等可执行内容只按纯文本（源码）处理，不渲染、不执行。
var textExts = map[string]string{
	".md": "text/markdown", ".markdown": "text/markdown", ".csv": "text/csv",
}

// codeExts 是按纯文本（text/plain）预览的常见文本与代码扩展名。
var codeExts = map[string]struct{}{
	".txt": {}, ".log": {}, ".text": {}, ".tsv": {}, ".json": {}, ".jsonl": {}, ".yaml": {}, ".yml": {}, ".toml": {},
	".ini": {}, ".cfg": {}, ".conf": {}, ".xml": {}, ".html": {}, ".htm": {}, ".svg": {}, ".css": {}, ".scss": {},
	".less": {}, ".js": {}, ".mjs": {}, ".cjs": {}, ".jsx": {}, ".ts": {}, ".tsx": {}, ".vue": {}, ".svelte": {},
	".go": {}, ".py": {}, ".rb": {}, ".rs": {}, ".java": {}, ".kt": {}, ".kts": {}, ".scala": {}, ".groovy": {},
	".gradle": {}, ".swift": {}, ".m": {}, ".mm": {}, ".c": {}, ".h": {}, ".cc": {}, ".cpp": {}, ".hpp": {},
	".cs": {}, ".php": {}, ".lua": {}, ".r": {}, ".dart": {}, ".pl": {}, ".ex": {}, ".exs": {}, ".erl": {},
	".hs": {}, ".clj": {}, ".sh": {}, ".bash": {}, ".zsh": {}, ".fish": {}, ".ps1": {}, ".bat": {}, ".sql": {},
	".graphql": {}, ".proto": {}, ".properties": {}, ".diff": {}, ".patch": {}, ".rst": {}, ".adoc": {}, ".tex": {},
}

// textNames 是没有扩展名、但按纯文本预览的常见文件名（小写）。
var textNames = map[string]struct{}{
	"makefile": {}, "dockerfile": {}, "readme": {}, "license": {}, "changelog": {}, "notice": {},
}

// imageExts 是按图片预览的扩展名 → MIME；内容嗅探必须得到同一类型。
var imageExts = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
}

// classifyArtifact 按扩展名白名单与内容嗅探决定 MIME：图片要求嗅探结果完全一致；文本要求是合法 UTF-8 且不含 NUL；
// 其他情况（未知扩展名、内容与扩展名不符）一律 application/octet-stream（只能下载）。
func classifyArtifact(name string, content []byte) string {
	ext := strings.ToLower(filepath.Ext(name))
	if expected, ok := imageExts[ext]; ok {
		if http.DetectContentType(content) == expected {
			return expected
		}
		return octetStream
	}
	expected, text := textExts[ext]
	if !text {
		_, code := codeExts[ext]
		_, plain := textNames[strings.ToLower(name)]
		if !code && !(ext == "" && plain) {
			return octetStream
		}
		expected = "text/plain"
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return octetStream
	}
	return expected
}

// within 判断 path 是否在 root 之内（两者都已是解析后的绝对路径；root 本身不算）。
func within(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// resolveRoots 解析允许目录的符号链接（例如 macOS 的 /var → /private/var）；解析失败的根丢弃。
func resolveRoots(roots []string) []string {
	resolved := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if real, err = filepath.Abs(real); err == nil {
			resolved = append(resolved, filepath.Clean(real))
		}
	}
	return resolved
}

// inspectArtifact 校验并读取 Agent 要发布的文件。raw 为相对路径时相对 roots[0]（工作目录）；roots 必须已解析符号链接。
// 返回的错误是 *artifactRejection（可告诉 Agent）或读文件时的系统错误。
func inspectArtifact(raw string, roots []string, maxBytes int64) (artifactFile, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return artifactFile{}, reject("path is required")
	}
	if len(raw) > maxArtifactPathBytes || strings.ContainsRune(raw, 0) || !utf8.ValidString(raw) {
		return artifactFile{}, reject("path is invalid")
	}
	if len(roots) == 0 {
		return artifactFile{}, reject("no artifact directories are available for this run")
	}
	requested := raw
	if !filepath.IsAbs(requested) {
		requested = filepath.Join(roots[0], requested)
	}
	requested = filepath.Clean(requested)
	// 先按字面路径检查敏感文件（例如指向普通文件的 .env 符号链接也拒绝），再解析符号链接检查一次。
	for _, root := range roots {
		if rel, ok := within(root, requested); ok && sensitivePath(rel) {
			return artifactFile{}, reject("refused: %s looks like a secret or repository metadata file", filepath.Base(requested))
		}
	}
	if sensitiveName(filepath.Base(requested)) {
		return artifactFile{}, reject("refused: %s looks like a secret or repository metadata file", filepath.Base(requested))
	}
	real, err := filepath.EvalSymlinks(requested)
	if err != nil {
		// 字面上就在允许目录之外的路径不报告是否存在，避免借此探测本机文件。
		lexical := false
		for _, root := range roots {
			if _, ok := within(root, requested); ok {
				lexical = true
			}
		}
		if !lexical {
			return artifactFile{}, reject("refused: the path is outside the managed run roots (the working directory and this run's artifact directory)")
		}
		if errors.Is(err, os.ErrNotExist) {
			return artifactFile{}, reject("file not found: %s", raw)
		}
		return artifactFile{}, reject("file is not accessible: %s", raw)
	}
	var rel string
	inside := false
	for _, root := range roots {
		if rel, inside = within(root, real); inside {
			break
		}
	}
	if !inside {
		return artifactFile{}, reject("refused: the path is outside the managed run roots (the working directory and this run's artifact directory)")
	}
	if sensitivePath(rel) {
		return artifactFile{}, reject("refused: %s looks like a secret or repository metadata file", filepath.Base(real))
	}
	info, err := os.Stat(real)
	if err != nil {
		return artifactFile{}, reject("file is not accessible: %s", raw)
	}
	if !info.Mode().IsRegular() {
		if info.IsDir() {
			return artifactFile{}, reject("refused: %s is a directory; publish files one by one", raw)
		}
		return artifactFile{}, reject("refused: %s is not a regular file", raw)
	}
	if info.Size() > maxBytes {
		return artifactFile{}, reject("refused: the file is %d bytes, larger than the %d MB limit", info.Size(), maxBytes>>20)
	}
	// O_NONBLOCK：校验后到打开前被换成管道时不会阻塞；打开后再核对是同一个普通文件。
	file, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return artifactFile{}, reject("file is not readable: %s", raw)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return artifactFile{}, reject("refused: %s changed while it was being read", raw)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return artifactFile{}, reject("file is not readable: %s", raw)
	}
	if int64(len(content)) > maxBytes {
		return artifactFile{}, reject("refused: the file is larger than the %d MB limit", maxBytes>>20)
	}
	sum := sha256.Sum256(content)
	name := safeAttachmentName(filepath.Base(real))
	return artifactFile{Path: real, Name: name, MIME: classifyArtifact(name, content), Content: content, SHA256: hex.EncodeToString(sum[:])}, nil
}

// artifactDisplayName 用 Agent 给的标题作为产物名：清洗后保留文件原扩展名（标题没带时补上），保证预览类型不变。
func artifactDisplayName(title, fileName string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return fileName
	}
	title = safeAttachmentName(boundUTF8(title, maxArtifactTitle))
	ext := filepath.Ext(fileName)
	if ext != "" && !strings.EqualFold(filepath.Ext(title), ext) {
		title += ext
	}
	return title
}

// artifactUploader 上传一个产物；测试可替换。
type artifactUploader func(ctx context.Context, artifactID, name, mimeType string, content []byte) (UploadedArtifact, error)

// artifactPublisher 是单个 Run 的发布工具状态：允许目录、已发布产物（按 sha256 去重）与上传器。
type artifactPublisher struct {
	roots    []string
	dir      string
	maxBytes int64
	upload   artifactUploader

	mu        sync.Mutex
	bySHA     map[string]UploadedArtifact
	published []string
}

func newArtifactPublisher(roots []string, dir string, upload artifactUploader) *artifactPublisher {
	return &artifactPublisher{roots: resolveRoots(roots), dir: dir, maxBytes: maxArtifactFileBytes, upload: upload, bySHA: map[string]UploadedArtifact{}}
}

// ids 返回已发布产物的 ID（按发布顺序，无重复），随结果上报。
func (p *artifactPublisher) ids() []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.published...)
}

type publishArguments struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

// call 是发布工具的 ToolCall：返回给 Agent 的文本与是否成功（拒绝原因写清楚，不含令牌与中心地址）。
// 同一 Run 内串行执行，保证去重与数量上限是原子的。
func (p *artifactPublisher) call(ctx context.Context, arguments json.RawMessage) (string, bool) {
	var args publishArguments
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if len(arguments) == 0 || decoder.Decode(&args) != nil {
		return "Invalid arguments: expected {\"path\": \"<file path>\", \"title\": \"<optional display name>\"}.", false
	}
	file, err := inspectArtifact(args.Path, p.roots, p.maxBytes)
	if err != nil {
		var rejection *artifactRejection
		if errors.As(err, &rejection) {
			return "Artifact not published: " + rejection.reason + ".", false
		}
		return "Artifact not published: the file could not be read.", false
	}
	name := artifactDisplayName(args.Title, file.Name)
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.bySHA[file.SHA256]; ok {
		return publishedText(existing, true), true
	}
	if len(p.published) >= maxRunArtifacts {
		return fmt.Sprintf("Artifact not published: the limit of %d artifacts per run has been reached.", maxRunArtifacts), false
	}
	uploaded, err := p.uploadWithRetry(ctx, name, file)
	if err != nil {
		return "Artifact not published: " + uploadErrorText(err), false
	}
	duplicate := false
	for _, id := range p.published {
		if id == uploaded.ID {
			duplicate = true
		}
	}
	if !duplicate {
		p.published = append(p.published, uploaded.ID)
	}
	p.bySHA[file.SHA256] = uploaded
	return publishedText(uploaded, duplicate), true
}

// uploadWithRetry 上传一次；网络错误或中心 5xx / 408 / 429 时换一个新的产物 ID 再试一次
// （中心按 sha256 去重，前一次其实已成功时返回已有产物）。
func (p *artifactPublisher) uploadWithRetry(ctx context.Context, name string, file artifactFile) (UploadedArtifact, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return UploadedArtifact{}, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		uploaded, err := p.upload(ctx, newUUID(), name, file.MIME, file.Content)
		if err == nil {
			return uploaded, nil
		}
		lastErr = err
		if permanent(err) || ctx.Err() != nil {
			break
		}
	}
	return UploadedArtifact{}, lastErr
}

func uploadErrorText(err error) string {
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusRequestEntityTooLarge:
		return "the organization's artifact limit (file size, total size per run or count) was reached."
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict:
		return "this run is no longer active on this computer."
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		return "this run was not found by the center."
	case errors.As(err, &apiErr):
		return fmt.Sprintf("the center rejected the upload (%d %s).", apiErr.Status, apiErr.Code)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return "the upload timed out or was cancelled."
	default:
		return "the upload failed (center unreachable)."
	}
}

func publishedText(artifact UploadedArtifact, duplicate bool) string {
	result := map[string]any{
		"ok": true, "artifact_id": artifact.ID, "name": artifact.Name, "size_bytes": artifact.SizeBytes,
		"previewable": artifact.MIMEType != "" && artifact.MIMEType != octetStream, "already_published": duplicate,
	}
	encoded, _ := json.Marshal(result)
	return "Artifact published; the user can open it from the conversation. " + string(encoded)
}

// publishTool 是发布工具的声明。
func publishTool() runtime.Tool {
	return runtime.Tool{
		Name: publishToolName,
		Description: "Deliver a file to the user as an artifact of this run. The user can preview Markdown, images, text, code and CSV, " +
			"and download any other file. Writing a local path in your reply does not deliver it. The file must be inside the working " +
			"directory or this run's artifact directory; secrets (.env, keys, .git, .ssh) are refused. At most 10 artifacts per run, 20 MB each.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":4096,"description":"File path, absolute or relative to the working directory."},"title":{"type":"string","maxLength":100,"description":"Optional display name; the file extension is kept."}},"required":["path"],"additionalProperties":false}`),
	}
}

// buildArtifactSection 生成提示词里的产物交付说明：产物目录、如何交付、「回复里写路径不算交付」。
func buildArtifactSection(dir string) string {
	var b strings.Builder
	b.WriteString("## Delivering files to the user\n")
	b.WriteString("To hand a file to the user, call openfox_publish_artifact with its path. Writing a local path in your reply does not deliver the file: the user cannot open paths on this computer.\n")
	fmt.Fprintf(&b, "Save files meant for the user in the working directory or in this run's artifact directory: %s\n", dir)
	b.WriteString("Only files inside these two directories can be published; secrets such as .env files, private keys, .git and .ssh are refused. ")
	fmt.Fprintf(&b, "At most %d artifacts per run, %d MB each. Markdown, PNG/JPEG/GIF/WebP images, plain text, code and CSV can be previewed; other files can be downloaded.\n\n", maxRunArtifacts, maxArtifactFileBytes>>20)
	return b.String()
}

func (s *Supervisor) artifactRoot() string {
	if root := strings.TrimSpace(s.config.ArtifactRoot); root != "" {
		return root
	}
	return filepath.Join(os.TempDir(), "openfox-run-artifacts")
}

// prepareArtifactDir 创建本次 Run 的产物目录并返回其绝对路径与清理函数（总是可调用）。失败时返回空路径：
// 不阻止 Run 执行，只是不声明发布工具。
func (r *runState) prepareArtifactDir() (string, func()) {
	noop := func() {}
	runID := r.offer.RunID
	if runID == "" || strings.ContainsAny(runID, `/\.`) {
		return "", noop
	}
	root, err := filepath.Abs(r.supervisor.artifactRoot())
	if err != nil {
		return "", noop
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		r.logger.Warn("supervisor_artifact_dir_failed", "error", err.Error())
		return "", noop
	}
	removeStaleAttachmentDirs(root, time.Now())
	dir := filepath.Join(root, runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.logger.Warn("supervisor_artifact_dir_failed", "error", err.Error())
		return "", noop
	}
	return filepath.Clean(dir), func() { _ = os.RemoveAll(dir) }
}

// uploadArtifact 把产物上传到中心（带本次 Run 的 generation，中心据此拒绝过期执行）。
func (r *runState) uploadArtifact(ctx context.Context, artifactID, name, mimeType string, content []byte) (UploadedArtifact, error) {
	uploaded, err := r.supervisor.config.Client.UploadRunArtifact(ctx, r.offer.OrganizationID, r.offer.RunID, r.offer.Generation, artifactID, name, mimeType, content)
	if err != nil {
		r.logger.Warn("supervisor_artifact_upload_failed", "error", r.redact.text(err.Error()))
		return UploadedArtifact{}, err
	}
	r.logger.Info("supervisor_artifact_published", "artifact_id", uploaded.ID, "size_bytes", uploaded.SizeBytes)
	return uploaded, nil
}

// combinedToolCall 在任务工具之上加发布工具：发布工具在本机执行，其余工具照旧转发中心；next 为 nil 时其余工具一律未知。
func (r *runState) combinedToolCall(next func(context.Context, string, string, json.RawMessage) (string, bool)) func(context.Context, string, string, json.RawMessage) (string, bool) {
	return func(ctx context.Context, name, callID string, arguments json.RawMessage) (string, bool) {
		if name == publishToolName {
			return r.artifacts.call(ctx, arguments)
		}
		if next == nil {
			return "unknown tool", false
		}
		return next(ctx, name, callID, arguments)
	}
}

// supportsTools 报告适配器能否把动态工具暴露给模型（runtime.ToolCapableAdapter）；没实现该接口的适配器按不支持处理。
func supportsTools(adapter runtime.Adapter) bool {
	typed, ok := adapter.(runtime.ToolCapableAdapter)
	return ok && typed.SupportsTools()
}
