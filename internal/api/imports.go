package api

// 导入：Jira CSV 上传、草稿配置、开始 / 取消、回滚。
// 写操作只有组织所有者 / 管理员；读按空间可见性。执行由 internal/imports 的后台 worker 完成。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/invitations"
	"openfox/internal/platform/apierror"
	"openfox/internal/platform/requestid"
	"openfox/internal/tenancy"
)

const (
	// importUploadLimit 为文件上限加 multipart 头部余量。
	importUploadLimit = imports.MaxFileBytes + 64<<10
	// importMultipartMemory 是 ParseMultipartForm 的内存上限，超出部分落临时文件。
	importMultipartMemory = 1 << 20
	// importConfigLimit 为配置请求体上限（300 列 + 500 状态 + 2000 用户的上限约 250 KiB）。
	importConfigLimit = 512 << 10
)

func (h *Handler) registerImportRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/imports"
	mux.HandleFunc("GET "+base, h.listImports)
	mux.HandleFunc("POST "+base+"/jira-csv", h.uploadJiraCsvImport)
	mux.HandleFunc("POST "+base+"/csv", h.uploadJiraCsvImport)
	mux.HandleFunc("POST "+base+"/jira-api", h.createJiraAPIImport)
	mux.HandleFunc("GET "+base+"/{provider}/projects", h.listImportProjects)
	mux.HandleFunc("PUT "+base+"/{importID}/sync", h.setImportSync)
	mux.HandleFunc("POST "+base+"/{provider}/project", h.createProjectImport)
	mux.HandleFunc("POST "+base+"/tapd-sources", h.listTAPDImportProjects)
	mux.HandleFunc("POST "+base+"/zentao-sources", h.zentaoImport)
	mux.HandleFunc("POST "+base+"/zentao-import", h.zentaoImport)
	h.registerAccountBindingRoutes(mux)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/external-accounts", h.listExternalAccounts)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/external-accounts/links", h.saveExternalAccountLinks)
	mux.HandleFunc("GET "+base+"/{importID}", h.getImport)
	mux.HandleFunc("PUT "+base+"/{importID}/config", h.putImportConfig)
	mux.HandleFunc("POST "+base+"/{importID}/start", h.startImport)
	mux.HandleFunc("POST "+base+"/{importID}/cancel", h.cancelImport)
	mux.HandleFunc("GET "+base+"/{importID}/rollback", h.getImportRollbackPreview)
	mux.HandleFunc("POST "+base+"/{importID}/rollback", h.rollbackImport)
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (h *Handler) listImports(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "limit is invalid")
			return
		}
		limit = n
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 200 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "cursor is invalid")
		return
	}
	var result imports.ListResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = (imports.Service{}).ListForConnection(ctx, tx, call.organizationID, call.selected.Actor, limit, cursor, r.URL.Query().Get("connection_id"))
		return err
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) uploadJiraCsvImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if !imports.CanManage(call.selected.Actor.Role) {
		writeImportError(w, r, imports.ErrForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, importUploadLimit)
	if err := r.ParseMultipartForm(importMultipartMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "IMPORT_FILE_TOO_LARGE", "file is larger than 10 MiB")
			return
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "multipart form is invalid")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	spaceID := strings.ToLower(strings.TrimSpace(r.FormValue("space_id")))
	newSpaceName := strings.TrimSpace(r.FormValue("new_space_name"))
	upload, header, err := r.FormFile("file")
	// space_id 与 new_space_name 二选一。
	if err != nil || (spaceID == "") == (newSpaceName == "") {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "file and exactly one of space_id or new_space_name are required")
		return
	}
	defer upload.Close()
	if header.Size > imports.MaxFileBytes {
		writeError(w, r, http.StatusRequestEntityTooLarge, "IMPORT_FILE_TOO_LARGE", "file is larger than 10 MiB")
		return
	}
	parsed, err := imports.ParseJiraCSV(io.LimitReader(upload, imports.MaxFileBytes+1))
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/csv") {
		parsed.Source = r.PostFormValue("source")
		if parsed.Source != "teambition_csv" && parsed.Source != "pingcode_csv" && parsed.Source != "tapd_excel" {
			writeImportError(w, r, imports.ErrInvalidRequest)
			return
		}
	}
	var newSpace *imports.NewSpace
	if newSpaceName != "" {
		ns, err := imports.NormalizeNewSpace(imports.NewSpace{Name: newSpaceName, Prefix: r.FormValue("new_space_prefix"),
			Visibility: r.FormValue("new_space_visibility")}, "")
		if err != nil {
			writeImportError(w, r, err)
			return
		}
		newSpace = &ns
	}
	var job imports.Job
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if newSpace != nil {
			if err := checkNewSpacePrefix(ctx, tx, service, call, newSpace.Prefix); err != nil {
				return err
			}
			job, err = (imports.Service{}).UploadNewSpace(ctx, tx, call.organizationID, call.selected.Actor, *newSpace, header.Filename, parsed)
			return err
		}
		if err := authorizeImportSpace(ctx, tx, service, call, spaceID); err != nil {
			return err
		}
		job, err = (imports.Service{}).Upload(ctx, tx, call.organizationID, call.selected.Actor, spaceID, header.Filename, parsed)
		return err
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (h *Handler) getImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var job imports.Job
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		job, err = (imports.Service{}).Get(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("importID"))
		return err
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

type putImportConfigRequest struct {
	ConfigVersion int64          `json:"config_version"`
	Config        imports.Config `json:"config"`
}

func (h *Handler) putImportConfig(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input putImportConfigRequest
	if err := decodeJSONLimit(w, r, importConfigLimit, &input); err != nil || input.ConfigVersion < 1 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var job imports.Job
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		job, err = (imports.Service{}).PutConfig(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("importID"), input.ConfigVersion, input.Config)
		return err
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

type startImportRequest struct {
	SyncTicket       string `json:"sync_ticket"`
	ConfigVersion    int64  `json:"config_version"`
	ConfirmOverwrite bool   `json:"confirm_overwrite"`
}

func (h *Handler) startImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input startImportRequest
	if err := decodeJSON(w, r, &input); err != nil || input.ConfigVersion < 1 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var result imports.StartResult
	var bindingErr error
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		if input.SyncTicket != "" {
			bindingErr = h.integrations.BindImportSite(ctx, tx, call.organizationID, r.PathValue("importID"), call.selected.Actor, input.SyncTicket)
			if bindingErr != nil {
				return bindingErr
			}
		}
		bindingErr = h.integrations.BindPendingTAPDImport(ctx, tx, call.organizationID, r.PathValue("importID"), call.selected.Actor)
		if bindingErr != nil {
			return bindingErr
		}
		result, err = (imports.Service{}).Start(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("importID"),
			input.ConfigVersion, input.ConfirmOverwrite)
		return err
	})
	if bindingErr != nil {
		writeIntegrationError(w, r, bindingErr)
		return
	}
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	job := result.Job
	if result.Started && len(result.Invites) > 0 {
		// 第二段：事务提交之后创建邀请并发邮件；任何失败只记日志，不影响导入。
		h.sendImportInvitations(r.Context(), call, job, result.Invites)
		// 邀请结果已写入 preview.invitations，重新读取返回。
		_ = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			fresh, err := (imports.Service{}).Get(ctx, tx, call.organizationID, call.selected.Actor, job.ID)
			if err == nil {
				job = fresh
			}
			return err
		})
	}
	writeJSON(w, http.StatusOK, job)
}

// sendImportInvitations 为 invite 模式里还不是成员的邮箱创建邀请（MEMBER，加入目标空间）并发送邮件。
func (h *Handler) sendImportInvitations(ctx context.Context, call taskCall, job imports.Job, pending []imports.PendingInvite) {
	service := invitations.Service{Pool: h.pool}
	role := strings.ToUpper(string(call.selected.Actor.Role))
	links := map[string]string{}
	results := make([]imports.InvitationResult, 0, len(pending))
	for _, p := range pending {
		invit, delivery, err := service.Create(ctx, call.selected.Selection, role,
			invitations.CreateInput{Email: p.Email, Role: "MEMBER", SpaceIDs: []string{job.Space()}})
		switch {
		case err == nil:
			links[p.AccountID] = invit.ID
			h.deliverInvitation(ctx, service, call.selected.Selection, &invit, delivery)
			result := "failed"
			if invit.DeliveryStatus == "SENT" {
				result = "sent"
			}
			results = append(results, imports.InvitationResult{SourceID: p.SourceID, Email: p.Email, Result: result})
		case errors.Is(err, invitations.ErrConflict):
			var existing string
			_ = h.pool.RunForOrganization(ctx, call.organizationID, tenancy.SourceUserRequest, staticCollaborationSelector(call.selected.Selection),
				func(ctx context.Context, tx pgx.Tx) error {
					var e error
					existing, e = (imports.Service{}).FindPendingInvitation(ctx, tx, p.Email)
					return e
				})
			if existing != "" {
				links[p.AccountID] = existing
			}
			results = append(results, imports.InvitationResult{SourceID: p.SourceID, Email: p.Email, Result: "already_invited"})
		case errors.Is(err, invitations.ErrAlreadyMember):
			results = append(results, imports.InvitationResult{SourceID: p.SourceID, Email: p.Email, Result: "already_member"})
		default:
			slog.Warn("import_invitation_failed", "import_id", job.ID, "error", err.Error())
			results = append(results, imports.InvitationResult{SourceID: p.SourceID, Email: p.Email, Result: "failed"})
		}
	}
	if err := h.pool.RunForOrganization(ctx, call.organizationID, tenancy.SourceUserRequest, staticCollaborationSelector(call.selected.Selection),
		func(ctx context.Context, tx pgx.Tx) error {
			return (imports.Service{}).RecordInvitations(ctx, tx, job.ID, links, results)
		}); err != nil {
		slog.Warn("import_invitation_record_failed", "import_id", job.ID, "error", err.Error())
	}
}

type importVersionRequest struct {
	Version int64 `json:"version"`
}

func (h *Handler) cancelImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input importVersionRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Version < 1 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var job imports.Job
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		job, err = (imports.Service{}).Cancel(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("importID"), input.Version)
		return err
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) getImportRollbackPreview(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var preview imports.RollbackPreview
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		preview, err = (imports.Service{}).RollbackPreview(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("importID"))
		return err
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *Handler) rollbackImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input importVersionRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Version < 1 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var job imports.Job
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		job, err = (imports.Service{}).Rollback(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("importID"), input.Version)
		return err
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// importReasonText 是 IMPORT_ROLLBACK_UNAVAILABLE 的原因文案。
func importRollbackMessage(reason string) string {
	switch reason {
	case "not_finished":
		return "import has not finished"
	case "expired":
		return "the 7-day rollback window has passed"
	case "rolled_back":
		return "import was already rolled back"
	case "nothing_created":
		return "import created no tasks"
	}
	return "rollback is unavailable"
}

func writeImportError(w http.ResponseWriter, r *http.Request, err error) {
	detail := imports.Detail(err)
	switch {
	case errors.Is(err, imports.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "you do not have permission for this action")
	case errors.Is(err, imports.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
	case errors.Is(err, imports.ErrSpaceNotFound):
		writeError(w, r, http.StatusNotFound, "SPACE_NOT_FOUND", "space is not available")
	case errors.Is(err, imports.ErrVersionConflict):
		var conflict *imports.ConflictError
		if errors.As(err, &conflict) {
			writeErrorDetails(w, r, http.StatusConflict, "VERSION_CONFLICT", "accounts changed; refresh and retry", map[string]any{"ids": conflict.IDs})
			return
		}
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "import changed; refresh and retry")
	case errors.Is(err, imports.ErrImportState):
		writeError(w, r, http.StatusConflict, "IMPORT_STATE", "import is not in a state that allows this action")
	case errors.Is(err, imports.ErrImportActive):
		writeError(w, r, http.StatusConflict, "IMPORT_ACTIVE", "another import is running in this space")
	case errors.Is(err, imports.ErrRollbackUnavailable):
		writeError(w, r, http.StatusConflict, "IMPORT_ROLLBACK_UNAVAILABLE", importRollbackMessage(detail))
	case errors.Is(err, imports.ErrRunActive):
		writeError(w, r, http.StatusConflict, "RUN_ACTIVE", "an agent run is still active on imported tasks")
	case errors.Is(err, imports.ErrInvalidConfig):
		writeError(w, r, http.StatusBadRequest, "INVALID_IMPORT_CONFIG", messageOr(detail, "import config is invalid"))
	case errors.Is(err, imports.ErrConfigIncomplete):
		writeError(w, r, http.StatusBadRequest, "IMPORT_CONFIG_INCOMPLETE", messageOr(detail, "import config is incomplete"))
	case errors.Is(err, imports.ErrOverwriteConfirmation):
		writeError(w, r, http.StatusBadRequest, "OVERWRITE_CONFIRMATION_REQUIRED", messageOr(detail, "overwrite must be confirmed"))
	case errors.Is(err, imports.ErrInvalidFile):
		writeError(w, r, http.StatusBadRequest, "INVALID_IMPORT_FILE", messageOr(imports.InvalidFileReason(err), "import file is invalid"))
	case errors.Is(err, imports.ErrInvalidRequest):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", messageOr(detail, "request is invalid"))
	case errors.Is(err, imports.ErrSecretsUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", "server encryption key is not configured")
	default:
		writeCollaborationError(w, r, err)
	}
}

func writeErrorDetails(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	requestID := requestid.FromContext(r.Context())
	if requestID == "" {
		requestID = r.Header.Get(requestid.Header)
	}
	apierror.WriteDetails(w, status, code, message, requestID, details)
}

func messageOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (h *Handler) setImportSync(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Mappings  map[string]string `json:"status_mappings"`
		Direction string            `json:"direction"`
		CheckNow  bool              `json:"check_now"`
		State     string            `json:"state"`
		Version   int64             `json:"version"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeImportError(w, r, imports.ErrInvalidRequest)
		return
	}
	var j imports.Job
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		if in.Mappings != nil {
			if in.State != "" || in.Direction != "" || in.CheckNow {
				return imports.ErrInvalidRequest
			}
			j, e = (imports.Service{}).SetSyncStatuses(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("importID"), in.Version, in.Mappings)
			return e
		}
		j, e = (imports.Service{}).SetProjectSync(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("importID"), in.State, in.Direction, in.CheckNow, in.Version)
		return e
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}
