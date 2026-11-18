package office

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxPreviewBytes = 32 << 20

var ErrPreviewUnavailable = errors.New("office preview unavailable")

// Preview 将已验证的 OOXML 发往管理员配置的隔离渲染服务，不接受文档指定的地址。
// 不在应用进程中运行 Office，不携带用户凭据，不重试、不跟随重定向。
type Preview struct {
	endpoint string
	client   *http.Client
	slots    chan struct{}
}

func ValidatePreviewURL(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("office preview URL must be an http(s) origin without credentials, path, query or fragment")
	}
	return nil
}

func NewPreview(origin string) *Preview {
	return &Preview{
		endpoint: strings.TrimRight(origin, "/"),
		client:   &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		slots:    make(chan struct{}, 2),
	}
}

func (p *Preview) PDF(ctx context.Context, raw []byte, kind string) ([]byte, error) {
	if p == nil || p.endpoint == "" || len(raw) == 0 || len(raw) > MaxBytes || (kind != "docx" && kind != "xlsx" && kind != "pptx") {
		return nil, ErrPreviewUnavailable
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return nil, ErrPreviewUnavailable
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("files", "document."+kind)
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(raw); err != nil {
		return nil, err
	}
	// 保留打印版式；禁用交互表单、批注导出和书签。
	for key, value := range map[string]string{"exportFormFields": "false", "exportNotes": "false", "exportBookmarks": "false"} {
		if err = form.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	if err = form.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/forms/libreoffice/convert", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	response, err := p.client.Do(req)
	if err != nil {
		return nil, ErrPreviewUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > MaxPreviewBytes {
		return nil, ErrPreviewUnavailable
	}
	pdf, err := io.ReadAll(io.LimitReader(response.Body, MaxPreviewBytes+1))
	if err != nil || len(pdf) > MaxPreviewBytes || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, ErrPreviewUnavailable
	}
	return pdf, nil
}
