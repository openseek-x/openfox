package api

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"openfox/internal/skills"
)

// 技能上传（multipart）与 URL 导入。两者都先授权再读取请求体 / 发起下载，内容都先在内存里校验（合计 ≤ 5 MB）。

const (
	// skillMultipartOverhead 为 multipart 的边界与表单字段预留的空间（最多 100 个文件的头部）。
	skillMultipartOverhead = 1 << 20
	// skillPathFieldLimit 限制 file_path 字段的大小（路径最多 256 个字符）。
	skillPathFieldLimit = 4 << 10
)

type uploadedSkillFile struct {
	name    string
	path    string
	hasPath bool
	content []byte
}

// readSkillUpload 读取 multipart：每个 file 部分可以紧跟一个 file_path 字段。读到的字节合计超过 5 MB 即停止。
func readSkillUpload(reader *multipart.Reader) ([]uploadedSkillFile, error) {
	var files []uploadedSkillFile
	budget := int64(skills.MaxTotalBytes)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		switch part.FormName() {
		case "file":
			if len(files) >= skills.MaxFiles {
				return nil, skills.ErrTooLarge
			}
			content, err := io.ReadAll(io.LimitReader(part, budget+1))
			if err != nil {
				return nil, err
			}
			if int64(len(content)) > budget {
				return nil, skills.ErrTooLarge
			}
			budget -= int64(len(content))
			files = append(files, uploadedSkillFile{name: part.FileName(), content: content})
		case "file_path":
			value, err := io.ReadAll(io.LimitReader(part, skillPathFieldLimit))
			if err != nil {
				return nil, err
			}
			if len(files) == 0 || files[len(files)-1].hasPath {
				return nil, skills.ErrInvalid
			}
			last := &files[len(files)-1]
			last.path, last.hasPath = strings.TrimSpace(string(value)), true
		default:
			_, _ = io.Copy(io.Discard, io.LimitReader(part, skillPathFieldLimit))
		}
	}
}

// uploadedFiles 把上传的内容整理成技能文件：单个 .zip 解压，单个 .md 作为 SKILL.md，其余按 file_path 当作文件夹。
func uploadedFiles(uploads []uploadedSkillFile) ([]skills.File, error) {
	if len(uploads) == 1 && !uploads[0].hasPath {
		switch strings.ToLower(path.Ext(uploads[0].name)) {
		case ".zip":
			return skills.UnzipFiles(uploads[0].content)
		case ".md", ".markdown":
			return []skills.File{{Path: skills.SkillFile, Content: uploads[0].content}}, nil
		}
		return nil, skills.ErrInvalid
	}
	files := make([]skills.File, 0, len(uploads))
	for _, upload := range uploads {
		if !upload.hasPath {
			return nil, skills.ErrInvalid
		}
		files = append(files, skills.File{Path: upload.path, Content: upload.content})
	}
	return files, nil
}

func (h *Handler) uploadSkill(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	spaceID := r.PathValue("spaceID")
	if err := h.authorizeSkillWrite(call, r, spaceID); err != nil {
		writeSkillError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, skills.MaxTotalBytes+skillMultipartOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "multipart body with a file field is required")
		return
	}
	uploads, err := readSkillUpload(reader)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || errors.Is(err, skills.ErrTooLarge) || errors.Is(err, skills.ErrInvalid) {
			writeSkillError(w, r, err)
		} else {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "multipart body with a file field is required")
		}
		return
	}
	if len(uploads) == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "multipart body with a file field is required")
		return
	}
	files, err := uploadedFiles(uploads)
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	pkg, err := skills.BuildPackage(files)
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	h.createSkillFromPackage(w, r, call, pkg, skills.Source{Kind: "upload"})
}

func (h *Handler) importSkillFromURL(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	rawURL := strings.TrimSpace(input.URL)
	// 先授权，再做静态校验，最后才发起下载。
	spaceID := r.PathValue("spaceID")
	if err := h.authorizeSkillWrite(call, r, spaceID); err != nil {
		writeSkillError(w, r, err)
		return
	}
	if _, err := skills.ParseURL(rawURL); err != nil {
		writeSkillError(w, r, err)
		return
	}
	result, err := (skills.Fetcher{}).Fetch(r.Context(), rawURL)
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	pkg, err := skills.BuildPackage(result.Files)
	if errors.Is(err, skills.ErrInvalid) {
		err = skills.FetchFailed("the downloaded content is not a valid skill")
	}
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	h.createSkillFromPackage(w, r, call, pkg, skills.Source{Kind: "url", Label: result.Host, URL: &rawURL})
}
