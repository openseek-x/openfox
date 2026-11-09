package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/devices"
	"openfox/internal/docs"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/skills"
)

// 技能接口：列表 / 详情 / 手动创建 / 保存修改 / 删除 / 读文件 / Agent 装配。
// 上传、URL 导入见 skills_upload.go，从 Runtime 导入与设备侧接口见 skills_runtime.go。
// 空间读写与停用判断复用文档库的 docAccess；只有创建者或组织管理员能改、删。

const (
	skillBase = docBase
	// skillJSONLimit 容纳保存修改的请求体：合计 5 MB 内容经转义后的 JSON。
	skillJSONLimit = 16 << 20
)

func (h *Handler) registerSkillRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET "+skillBase+"/skills", h.listSkills)
	mux.HandleFunc("POST "+skillBase+"/skills", h.createSkill)
	mux.HandleFunc("POST "+skillBase+"/skills/upload", h.uploadSkill)
	mux.HandleFunc("POST "+skillBase+"/skills/url-imports", h.importSkillFromURL)
	mux.HandleFunc("POST "+skillBase+"/skills/runtime-imports", h.createSkillRuntimeImport)
	// GET /skills/runtime-imports/{commandID} 与 GET /skills/{skillID}/files 在 ServeMux 里互相冲突，合并后分发。
	mux.HandleFunc("GET "+skillBase+"/skills/{first}/{second}", h.getSkillSubresource)
	mux.HandleFunc("POST "+skillBase+"/skills/runtime-imports/{commandID}/cancel", h.cancelSkillRuntimeImport)
	mux.HandleFunc("GET "+skillBase+"/skills/{skillID}", h.getSkill)
	mux.HandleFunc("DELETE "+skillBase+"/skills/{skillID}", h.deleteSkill)
	mux.HandleFunc("PUT "+skillBase+"/skills/{skillID}/files", h.saveSkillFiles)
	mux.HandleFunc("GET "+skillBase+"/agents/{agentID}/skills", h.listAgentSkills)
	mux.HandleFunc("PUT "+skillBase+"/agents/{agentID}/skills", h.setAgentSkills)
	mux.HandleFunc("GET /v1/device/organizations/{organizationID}/runs/{runID}/skills/{skillID}", h.getDeviceRunSkill)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/commands/{commandID}/skills", h.uploadDeviceImportedSkill)
}

// getSkillSubresource 分发 GET /skills/runtime-imports/{commandID} 与 GET /skills/{skillID}/files。
func (h *Handler) getSkillSubresource(w http.ResponseWriter, r *http.Request) {
	first, second := r.PathValue("first"), r.PathValue("second")
	switch {
	case first == "runtime-imports":
		r.SetPathValue("commandID", second)
		h.getSkillRuntimeImport(w, r)
	case second == "files":
		r.SetPathValue("skillID", first)
		h.getSkillFile(w, r)
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
	}
}

// writeSkillError 把技能、Agent、电脑相关的错误映射为接口错误。
func writeSkillError(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeCollaborationError(w, r, skills.ErrTooLarge)
	case errors.Is(err, agents.ErrNotFound), errors.Is(err, agents.ErrForbidden), errors.Is(err, agents.ErrArchived):
		writeAgentError(w, r, err)
	case errors.Is(err, devices.ErrInvalidRequest), errors.Is(err, devices.ErrDeviceNotFound), errors.Is(err, devices.ErrGrantNotFound),
		errors.Is(err, devices.ErrCommandConflict):
		writeDeviceManagementError(w, r, err)
	default:
		writeCollaborationError(w, r, err)
	}
}

// skillPathID 读取路径里的技能 ID；格式不对与不存在一样返回 404。
func skillPathID(r *http.Request, name string) (string, error) {
	value := r.PathValue(name)
	if !membership.ValidateUUID(value) {
		return "", skills.ErrNotFound
	}
	return value, nil
}

// canModifySkill：空间可写且未停用，并且是创建者或组织管理员。
func (c taskCall) canModifySkill(access docs.Access, createdBy string) bool {
	return c.requireModifySkill(access, createdBy) == nil
}

func (c taskCall) requireModifySkill(access docs.Access, createdBy string) error {
	if err := access.RequireSetWrite(); err != nil {
		return err
	}
	role := c.selected.Actor.Role
	if createdBy == c.selected.Actor.UserID || role == policy.OrganizationRoleOwner || role == policy.OrganizationRoleAdmin {
		return nil
	}
	return skills.ErrForbidden
}

func (c taskCall) markCanEdit(access docs.Access, items []skills.Skill) {
	for i := range items {
		items[i].CanEdit = !items[i].AgentOwned && c.canModifySkill(access, items[i].CreatedBy.ID)
	}
}

// authorizeSkillWrite 在流式读取请求体或下载之前先做一次授权。
func (h *Handler) authorizeSkillWrite(call taskCall, r *http.Request, spaceID string) error {
	return call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, spaceID)
		if err != nil {
			return err
		}
		return access.RequireSetWrite()
	})
}

func (h *Handler) listSkills(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	values := r.URL.Query()
	query := skills.ListQuery{Q: strings.TrimSpace(values.Get("q")), Sort: values.Get("sort"), CreatedBy: values.Get("created_by")}
	if utf8.RuneCountInString(query.Q) > skills.MaxSearchRunes {
		writeCollaborationError(w, r, skills.ErrQueryInvalid)
		return
	}
	switch query.Sort {
	case "", "usage", "updated", "created":
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "sort is invalid")
		return
	}
	if query.CreatedBy != "" && !membership.ValidateUUID(query.CreatedBy) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "created_by is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	var items []skills.Skill
	canCreate := false
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, spaceID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		if items, err = skills.List(ctx, tx, spaceID, call.selected.Actor.UserID, query); err != nil {
			return err
		}
		call.markCanEdit(access, items)
		canCreate = access.RequireSetWrite() == nil
		return nil
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": items, "can_create": canCreate})
}

func (h *Handler) getSkill(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	skillID, err := skillPathID(r, "skillID")
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	spaceID := r.PathValue("spaceID")
	var detail skills.Detail
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, skillID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		if detail, err = skills.Get(ctx, tx, spaceID, skillID, call.selected.Actor.UserID); err != nil {
			return err
		}
		detail.CanEdit = call.canModifySkill(access, detail.CreatedBy.ID)
		return nil
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type skillCreateRequest struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

func (h *Handler) createSkill(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	var input skillCreateRequest
	if err := decodeDocJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if !skills.ValidName(input.Name) {
		writeCollaborationError(w, r, skills.ErrNameInvalid)
		return
	}
	if utf8.RuneCountInString(input.Description) > skills.MaxDescriptionRunes || len(input.Instructions) > skills.MaxInstructionBytes {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "description or instructions are too long")
		return
	}
	pkg, err := skills.BuildPackage([]skills.File{{Path: skills.SkillFile, Content: skills.BuildSkillMD(input.Name, strings.TrimSpace(input.Description), input.Instructions)}})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	h.createSkillFromPackage(w, r, call, pkg, skills.Source{Kind: "manual"})
}

// createSkillFromPackage 把技能内容写入存储并在事务里建立技能，响应 201 与详情。
// 存储写入在数据库事务之外进行；事务失败时删除刚写入的存储文件。
func (h *Handler) createSkillFromPackage(w http.ResponseWriter, r *http.Request, call taskCall, pkg skills.Package, source skills.Source) {
	spaceID := r.PathValue("spaceID")
	skillID, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	entries, created, err := skills.StoreFiles(r.Context(), h.delivery.Storage, call.organizationID, skillID, pkg.Files)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	var detail skills.Detail
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, skillID)
		if err != nil {
			return err
		}
		if err := access.RequireSetWrite(); err != nil {
			return err
		}
		userID := call.selected.Actor.UserID
		if err := skills.Create(ctx, tx, skills.CreateInput{
			ID: skillID, SpaceID: spaceID, UserID: userID, Source: source, Package: pkg, Entries: entries,
		}); err != nil {
			return err
		}
		if detail, err = skills.Get(ctx, tx, spaceID, skillID, userID); err != nil {
			return err
		}
		detail.CanEdit = true
		return nil
	})
	if err != nil {
		skills.DeleteKeys(r.Context(), h.delivery.Storage, created)
		writeSkillError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (h *Handler) deleteSkill(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	skillID, err := skillPathID(r, "skillID")
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	version, err := docVersion(r.URL.Query().Get("version"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spaceID := r.PathValue("spaceID")
	var keys []string
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, skillID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		meta, err := skills.Lock(ctx, tx, spaceID, skillID, true)
		if err != nil {
			return err
		}
		if err := call.requireModifySkill(access, meta.CreatedBy); err != nil {
			return err
		}
		if err := meta.CheckVersion(int64(version)); err != nil {
			return err
		}
		keys, err = skills.Remove(ctx, tx, spaceID, meta)
		return err
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	if h.delivery.Storage != nil {
		skills.DeleteKeys(r.Context(), h.delivery.Storage, keys)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getSkillFile(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	skillID, err := skillPathID(r, "skillID")
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	path := r.URL.Query().Get("path")
	if err := skills.ValidatePath(path); err != nil {
		writeSkillError(w, r, skills.ErrFileNotFound)
		return
	}
	spaceID := r.PathValue("spaceID")
	var entry skills.Entry
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, skillID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		entry, err = skills.LoadEntry(ctx, tx, spaceID, skillID, path)
		return err
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	content, err := skills.ReadBlob(r.Context(), h.delivery.Storage, entry)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "the file could not be read")
		return
	}
	result := map[string]any{"path": entry.Path, "size_bytes": entry.Size, "binary": true, "content": ""}
	if utf8.Valid(content) && !strings.ContainsRune(string(content), 0) {
		result["binary"], result["content"] = false, string(content)
	}
	writeJSON(w, http.StatusOK, result)
}

type skillSaveChange struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
	Delete  bool    `json:"delete"`
}

type skillSaveRequest struct {
	Version uint64            `json:"version"`
	Changes []skillSaveChange `json:"changes"`
}

func (h *Handler) saveSkillFiles(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	skillID, err := skillPathID(r, "skillID")
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	var input skillSaveRequest
	r.Body = http.MaxBytesReader(w, r.Body, skillJSONLimit)
	if err := decodeStrictJSON(r, &input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeCollaborationError(w, r, skills.ErrTooLarge)
			return
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if input.Version == 0 || len(input.Changes) == 0 || len(input.Changes) > skills.MaxFiles {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "version and 1-100 changes are required")
		return
	}
	// 先在内存里校验每一项：路径、删除 / 内容二选一、SKILL.md 的 frontmatter。
	var parsed *skills.Parsed
	files := make([]skills.File, 0, len(input.Changes))
	deletes := map[string]bool{}
	for _, change := range input.Changes {
		if err := skills.ValidatePath(change.Path); err != nil {
			writeSkillError(w, r, err)
			return
		}
		if change.Delete {
			if change.Content != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "a deleted file cannot carry content")
				return
			}
			deletes[change.Path] = true
			continue
		}
		if change.Content == nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "content is required unless delete is true")
			return
		}
		content := []byte(*change.Content)
		if len(content) > skills.MaxTotalBytes {
			writeCollaborationError(w, r, skills.ErrTooLarge)
			return
		}
		if change.Path == skills.SkillFile {
			result, err := skills.ParseSkillMD(content)
			if err != nil {
				writeSkillError(w, r, err)
				return
			}
			parsed = &result
		}
		files = append(files, skills.File{Path: change.Path, Content: content})
	}
	spaceID := r.PathValue("spaceID")
	// 先授权，避免无权限的请求也写存储。
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, skillID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		meta, err := skills.Lock(ctx, tx, spaceID, skillID, false)
		if err != nil {
			return err
		}
		return call.requireModifySkill(access, meta.CreatedBy)
	}); err != nil {
		writeSkillError(w, r, err)
		return
	}
	entries, created, err := skills.StoreFiles(r.Context(), h.delivery.Storage, call.organizationID, skillID, files)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	changes := make([]skills.Change, 0, len(input.Changes))
	for _, entry := range entries {
		changes = append(changes, skills.Change{Path: entry.Path, Entry: entry})
	}
	for _, change := range input.Changes {
		if deletes[change.Path] {
			changes = append(changes, skills.Change{Path: change.Path, Delete: true})
		}
	}
	var detail skills.Detail
	var removed []string
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, skillID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		meta, err := skills.Lock(ctx, tx, spaceID, skillID, true)
		if err != nil {
			return err
		}
		if err := call.requireModifySkill(access, meta.CreatedBy); err != nil {
			return err
		}
		if err := meta.CheckVersion(int64(input.Version)); err != nil {
			return err
		}
		next, err := skills.ApplyChanges(meta.Entries, changes)
		if err != nil {
			return err
		}
		name, description := meta.Name, meta.Description
		if parsed != nil {
			name, description = parsed.Name, parsed.Description
		}
		userID := call.selected.Actor.UserID
		if removed, err = skills.Replace(ctx, tx, meta, skills.ReplaceInput{
			SpaceID: spaceID, SkillID: skillID, UserID: userID, Name: name, Description: description, Entries: next,
		}); err != nil {
			return err
		}
		if detail, err = skills.Get(ctx, tx, spaceID, skillID, userID); err != nil {
			return err
		}
		detail.CanEdit = true
		return nil
	})
	if err != nil {
		skills.DeleteKeys(r.Context(), h.delivery.Storage, created)
		writeSkillError(w, r, err)
		return
	}
	skills.DeleteKeys(r.Context(), h.delivery.Storage, removed)
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) skillAgentActor(call taskCall, r *http.Request) agents.Actor {
	return agents.Actor{
		UserID: call.selected.Actor.UserID, Role: call.selected.Actor.Role,
		AuthorizationVersion: call.selected.Actor.AuthorizationVersion, RequestID: agentRequestID(r),
	}
}

func (h *Handler) listAgentSkills(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, agentID := r.PathValue("spaceID"), r.PathValue("agentID")
	if !membership.ValidateUUID(agentID) {
		writeAgentError(w, r, agents.ErrNotFound)
		return
	}
	var items []skills.Skill
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, spaceID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		if _, err := agents.RequireReadableAgent(ctx, tx, call.organizationID, spaceID, agentID, h.skillAgentActor(call, r)); err != nil {
			return err
		}
		if items, err = skills.ListForAgent(ctx, tx, spaceID, agentID, call.selected.Actor.UserID); err != nil {
			return err
		}
		call.markCanEdit(access, items)
		return nil
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": items})
}

func (h *Handler) setAgentSkills(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, agentID := r.PathValue("spaceID"), r.PathValue("agentID")
	if !membership.ValidateUUID(agentID) {
		writeAgentError(w, r, agents.ErrNotFound)
		return
	}
	var input struct {
		SkillIDs *[]string `json:"skill_ids"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.SkillIDs == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var items []skills.Skill
	var removed []string
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, agentID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		// 能编辑该 Agent（所有者或空间管理者）：复用 Agent 现有的判断。
		if _, err := agents.LockEditableAgent(ctx, tx, call.organizationID, spaceID, agentID, h.skillAgentActor(call, r)); err != nil {
			return err
		}
		if access.Retired != nil {
			return access.Retired
		}
		userID := call.selected.Actor.UserID
		if removed, err = skills.SetForAgent(ctx, tx, spaceID, agentID, userID, *input.SkillIDs); err != nil {
			return err
		}
		if items, err = skills.ListForAgent(ctx, tx, spaceID, agentID, userID); err != nil {
			return err
		}
		call.markCanEdit(access, items)
		return nil
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	skills.DeleteKeys(r.Context(), h.delivery.Storage, removed)
	writeJSON(w, http.StatusOK, map[string]any{"skills": items})
}

// decodeStrictJSON 解码已加上大小限制的请求体：拒绝未知字段与多余内容。
func decodeStrictJSON(r *http.Request, target any) error {
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
