package api

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/docs"
	"openfox/internal/market"
	"openfox/internal/membership"

	"openfox/internal/skills"
	"openfox/internal/tenancy"
)

// 市场接口。目录是全平台共用的只读数据：读取只要求组织成员身份（租户事务内完成），不需要空间权限。
// 添加 / 更新 / 应用模板把市场文件复制成空间技能，复用 5c 的创建、替换与装配（权限判断同 5c）。

const marketBase = "/v1/organizations/{organizationID}/market"

func (h *Handler) registerMarketRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET "+marketBase+"/catalog", h.getMarketCatalog)
	mux.HandleFunc("GET "+marketBase+"/skills", h.listMarketSkills)
	mux.HandleFunc("GET "+marketBase+"/skills/{marketSkillID}", h.getMarketSkill)
	mux.HandleFunc("GET "+marketBase+"/skills/{marketSkillID}/file", h.getMarketSkillFile)
	mux.HandleFunc("GET "+marketBase+"/skills/{marketSkillID}/download", h.downloadMarketSkillFile)
	mux.HandleFunc("GET "+marketBase+"/templates", h.listMarketTemplates)
	mux.HandleFunc("GET "+marketBase+"/templates/{marketTemplateID}", h.getMarketTemplate)
	mux.HandleFunc("POST "+skillBase+"/skills/market", h.addMarketSkill)
	mux.HandleFunc("POST "+skillBase+"/skills/{skillID}/market-update", h.updateMarketSkill)
	mux.HandleFunc("POST "+skillBase+"/agents/{agentID}/market-template", h.applyMarketTemplate)
	mux.HandleFunc("PUT "+skillBase+"/agents/{agentID}/market-skills/{marketSkillID}", h.installAgentMarketSkill)
	mux.HandleFunc("GET "+marketBase+"/agent-targets", h.listMarketAgentTargets)
}

// marketRead 在租户事务里执行读取并写出 JSON。
func marketRead[T any](h *Handler, w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, tx pgx.Tx) (T, error)) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var result T
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = fn(ctx, tx)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) getMarketCatalog(w http.ResponseWriter, r *http.Request) {
	marketRead(h, w, r, market.GetCatalog)
}

func (h *Handler) listMarketSkills(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	query := market.ListQuery{
		Category: values.Get("category"), Q: strings.TrimSpace(values.Get("q")), Sort: values.Get("sort"), Cursor: values.Get("cursor"),
	}
	limit, err := marketLimit(values.Get("limit"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	query.Limit = limit
	if ids := values.Get("ids"); ids != "" {
		for _, id := range strings.Split(ids, ",") {
			if id = strings.TrimSpace(id); id != "" {
				query.IDs = append(query.IDs, id)
			}
		}
		if len(query.IDs) == 0 {
			writeCollaborationError(w, r, market.ErrInvalidQuery)
			return
		}
	}
	marketRead(h, w, r, func(ctx context.Context, tx pgx.Tx) (market.SkillPage, error) {
		return market.ListSkills(ctx, tx, query)
	})
}

// marketLimit 解析 limit：缺省返回 0（由市场包取默认值）。
func marketLimit(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > market.MaxListLimit {
		return 0, market.ErrInvalidQuery
	}
	return limit, nil
}

func (h *Handler) getMarketSkill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("marketSkillID")
	marketRead(h, w, r, func(ctx context.Context, tx pgx.Tx) (market.SkillDetail, error) {
		return market.GetSkill(ctx, tx, id)
	})
}

func (h *Handler) listMarketTemplates(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	limit, err := marketLimit(values.Get("limit"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	category, q, cursor := values.Get("category"), strings.TrimSpace(values.Get("q")), values.Get("cursor")
	marketRead(h, w, r, func(ctx context.Context, tx pgx.Tx) (market.TemplatePage, error) {
		return market.ListTemplates(ctx, tx, category, q, limit, cursor)
	})
}

func (h *Handler) getMarketTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("marketTemplateID")
	marketRead(h, w, r, func(ctx context.Context, tx pgx.Tx) (market.TemplateDetail, error) {
		return market.GetTemplate(ctx, tx, id)
	})
}

// readMarketFile 校验成员身份后读出一个市场文件的记录与内容。存储未配置或读取失败返回 503。
func (h *Handler) readMarketFile(w http.ResponseWriter, r *http.Request) (market.FileRecord, []byte, bool) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return market.FileRecord{}, nil, false
	}
	id, filePath := r.PathValue("marketSkillID"), r.URL.Query().Get("path")
	if err := skills.ValidatePath(filePath); err != nil {
		writeCollaborationError(w, r, market.ErrFileNotFound)
		return market.FileRecord{}, nil, false
	}
	var record market.FileRecord
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		record, err = market.LoadFile(ctx, tx, id, filePath)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return market.FileRecord{}, nil, false
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return market.FileRecord{}, nil, false
	}
	content, err := skills.ReadBlob(r.Context(), h.delivery.Storage, record.Entry)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "the file could not be read")
		return market.FileRecord{}, nil, false
	}
	return record, content, true
}

func (h *Handler) getMarketSkillFile(w http.ResponseWriter, r *http.Request) {
	record, content, ok := h.readMarketFile(w, r)
	if !ok {
		return
	}
	result := market.FileContent{Path: record.Path, SizeBytes: record.Size, Mime: record.Mime, Binary: true}
	if market.IsText(content) {
		result.Binary = false
		result.Content, result.Truncated = market.Preview(content)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) downloadMarketSkillFile(w http.ResponseWriter, r *http.Request) {
	record, content, ok := h.readMarketFile(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", docs.ContentDisposition("attachment", path.Base(record.Path)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

type marketSkillRequest struct {
	MarketSkillID string `json:"market_skill_id"`
}

// addMarketSkill 把市场技能复制到空间。先授权并读出市场内容，再复用 createSkillFromPackage 写存储与建技能。
func (h *Handler) addMarketSkill(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	var input marketSkillRequest
	if err := decodeJSON(w, r, &input); err != nil || !market.IDPattern.MatchString(input.MarketSkillID) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	var src market.Source
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, spaceID)
		if err != nil {
			return err
		}
		if err := access.RequireSetWrite(); err != nil {
			return err
		}
		if id, found, err := skills.FindMarket(ctx, tx, spaceID, input.MarketSkillID); err != nil {
			return err
		} else if found {
			return skills.ErrMarketExists.WithDetails(map[string]any{"skill_id": id})
		}
		src, err = market.LoadSource(ctx, tx, input.MarketSkillID)
		return err
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	files, err := market.ReadFiles(r.Context(), h.delivery.Storage, src)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "the market files could not be read")
		return
	}
	version := src.Version
	h.createSkillFromPackage(w, r, call, market.SpacePackage(src, files), skills.Source{
		Kind: "market", Label: market.SourceLabel(src), MarketSkillID: &input.MarketSkillID, MarketVersion: &version,
	})
}

// updateMarketSkill 把空间技能更新到市场当前版本：文件整体替换，名称不变，描述取新版本。
func (h *Handler) updateMarketSkill(w http.ResponseWriter, r *http.Request) {
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
	var input struct {
		Version uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	// check 在已锁定的技能上核对权限、版本与市场来源；返回市场内容。
	var src market.Source
	check := func(ctx context.Context, tx pgx.Tx, service collaboration.Service, forUpdate bool) (skills.Meta, error) {
		access, err := call.docAccess(ctx, tx, service, spaceID, skillID)
		if err != nil {
			return skills.Meta{}, err
		}
		if err := access.RequireRead(); err != nil {
			return skills.Meta{}, err
		}
		meta, err := skills.Lock(ctx, tx, spaceID, skillID, forUpdate)
		if err != nil {
			return skills.Meta{}, err
		}
		if err := call.requireModifySkill(access, meta.CreatedBy); err != nil {
			return skills.Meta{}, err
		}
		if err := meta.CheckVersion(int64(input.Version)); err != nil {
			return skills.Meta{}, err
		}
		if meta.MarketSkillID == nil || meta.MarketVersion == nil {
			return skills.Meta{}, skills.ErrNotFromMarket
		}
		return meta, nil
	}
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		meta, err := check(ctx, tx, service, false)
		if err != nil {
			return err
		}
		if src, err = market.LoadSource(ctx, tx, *meta.MarketSkillID); err != nil {
			return err
		}
		if src.Version == *meta.MarketVersion {
			return skills.ErrUpToDate
		}
		return nil
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	files, err := market.ReadFiles(r.Context(), h.delivery.Storage, src)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "the market files could not be read")
		return
	}
	pkg := market.SpacePackage(src, files)
	entries, created, err := skills.StoreFiles(r.Context(), h.delivery.Storage, call.organizationID, skillID, pkg.Files)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	var detail skills.Detail
	var removed []string
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		meta, err := check(ctx, tx, service, true)
		if err != nil {
			return err
		}
		// 复制期间市场又发了新版本（或技能被撤下）：让调用方重试，避免记录的版本与文件不一致。
		latest, err := market.LatestVersion(ctx, tx, *meta.MarketSkillID)
		if err != nil {
			return err
		}
		if latest != src.Version {
			return collaboration.ErrVersionConflict
		}
		userID := call.selected.Actor.UserID
		if removed, err = skills.Replace(ctx, tx, meta, skills.ReplaceInput{
			SpaceID: spaceID, SkillID: skillID, UserID: userID, Name: meta.Name, Description: pkg.Description,
			Entries: entries, MarketVersion: &src.Version,
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

type marketApplyItem struct {
	MarketSkillID string  `json:"market_skill_id"`
	Status        string  `json:"status"`
	Reason        string  `json:"reason"`
	SkillID       *string `json:"skill_id"`
}

// marketPrepared 是要新增到空间的市场技能：文件已写入存储，等待事务里建立。
type marketPrepared struct {
	skillID string
	source  skills.Source
	pkg     skills.Package
	entries []skills.Entry
	keys    []string
	kept    bool
}

// applyMarketTemplate 把模板引用的空间技能装配给 Agent。三步：
//  1. 事务一：授权，找出需要新增的技能（市场里有、空间里没有、Agent 还有装配名额），读出市场内容。
//  2. 事务之外：把文件写入存储。
//  3. 事务二：重新授权，逐个建立技能（重名、超上限用保存点记为 skipped）并一次性设置 Agent 装配。
func (h *Handler) applyMarketTemplate(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	spaceID, agentID := r.PathValue("spaceID"), r.PathValue("agentID")
	if !membership.ValidateUUID(agentID) {
		writeAgentError(w, r, agents.ErrNotFound)
		return
	}
	var input struct {
		MarketTemplateID string `json:"market_template_id"`
	}
	if decodeJSON(w, r, &input) != nil || !market.IDPattern.MatchString(input.MarketTemplateID) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}

	// authorize 在事务里授权：空间可写（含停用判断）且能编辑该 Agent。
	authorize := func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		_, err := h.lockMarketAgent(ctx, tx, call, r, service)
		return err
	}

	var refs []string
	missing := map[string]bool{}
	var sources []market.Source
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		refs, sources = nil, nil
		missing = map[string]bool{}
		if err := authorize(ctx, tx, service); err != nil {
			return err
		}
		var err error
		if refs, err = market.TemplateRefs(ctx, tx, input.MarketTemplateID); err != nil {
			return err
		}
		existing, err := skills.TemplateMarketSkills(ctx, tx, spaceID, agentID)
		if err != nil {
			return err
		}
		current, err := skills.AgentSkillIDs(ctx, tx, agentID)
		if err != nil {
			return err
		}
		attached := map[string]bool{}
		for _, id := range current {
			attached[id] = true
		}
		slots := skills.MaxAgentSkills - len(current)
		for _, id := range refs {
			src, err := market.LoadSource(ctx, tx, id)
			if errors.Is(err, market.ErrNotFound) {
				missing[id] = true
				continue
			}
			if err != nil {
				return err
			}
			skillID, inSpace := existing[id]
			if inSpace && attached[skillID] {
				continue
			}
			if slots <= 0 {
				continue
			}
			slots--
			if !inSpace {
				sources = append(sources, src)
			}
		}
		return nil
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}

	prepared := map[string]*marketPrepared{}
	cleanup := func() {
		for _, item := range prepared {
			if !item.kept {
				skills.DeleteKeys(r.Context(), h.delivery.Storage, item.keys)
			}
		}
	}
	for _, src := range sources {
		files, err := market.ReadFiles(r.Context(), h.delivery.Storage, src)
		if err != nil {
			cleanup()
			writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "the market files could not be read")
			return
		}
		skillID, err := collaboration.NewID()
		if err != nil {
			cleanup()
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
			return
		}
		pkg := market.SpacePackage(src, files)
		entries, keys, err := skills.StoreFiles(r.Context(), h.delivery.Storage, call.organizationID, skillID, pkg.Files)
		if err != nil {
			cleanup()
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
			return
		}
		version, id := src.Version, src.ID
		prepared[src.ID] = &marketPrepared{
			skillID: skillID, pkg: pkg, entries: entries, keys: keys,
			source: skills.Source{Kind: "market", Label: market.SourceLabel(src), MarketSkillID: &id, MarketVersion: &version},
		}
	}

	var items []marketApplyItem
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		items = nil
		for _, item := range prepared {
			item.kept = false
		}
		if err := authorize(ctx, tx, service); err != nil {
			return err
		}
		// 与同一空间的其他安装串行，已有副本即使由另一个 Agent 刚创建也会复用。
		if err := skills.LockSpaceCatalog(ctx, tx, spaceID); err != nil {
			return err
		}
		existing, err := skills.TemplateMarketSkills(ctx, tx, spaceID, agentID)
		if err != nil {
			return err
		}
		current, err := skills.AgentSkillIDs(ctx, tx, agentID)
		if err != nil {
			return err
		}
		attached := map[string]bool{}
		for _, id := range current {
			attached[id] = true
		}
		next := append([]string(nil), current...)
		userID := call.selected.Actor.UserID
		for _, id := range refs {
			entry := marketApplyItem{MarketSkillID: id}
			skillID, inSpace := existing[id]
			if inSpace {
				entry.SkillID = &skillID
			}
			switch {
			case missing[id]:
				entry.Status = "missing"
			case inSpace && attached[skillID]:
				entry.Status = "attached"
			case len(next) >= skills.MaxAgentSkills:
				entry.Status, entry.Reason = "skipped", "AGENT_SKILL_LIMIT"
			case inSpace:
				entry.Status = "reused"
				next = append(next, skillID)
			default:
				item := prepared[id]
				if item == nil {
					// 两次事务之间空间或 Agent 的状态变了：让调用方重试。
					return collaboration.ErrVersionConflict
				}
				// 保存点：重名、超上限只回滚这一个技能，不影响其他项。
				savepoint, err := tx.Begin(ctx)
				if err != nil {
					return err
				}
				createErr := skills.Create(ctx, savepoint, skills.CreateInput{
					ID: item.skillID, SpaceID: spaceID, UserID: userID, Source: item.source, Package: item.pkg, Entries: item.entries,
				})
				switch {
				case createErr == nil:
					if err := savepoint.Commit(ctx); err != nil {
						return err
					}
					item.kept = true
					entry.Status, entry.SkillID = "added", &item.skillID
					next = append(next, item.skillID)
				case errors.Is(createErr, skills.ErrNameTaken):
					_ = savepoint.Rollback(ctx)
					entry.Status, entry.Reason = "skipped", "SKILL_NAME_TAKEN"
				case errors.Is(createErr, skills.ErrLimit):
					_ = savepoint.Rollback(ctx)
					entry.Status, entry.Reason = "skipped", "SKILL_LIMIT"
				default:
					_ = savepoint.Rollback(ctx)
					return createErr
				}
			}
			items = append(items, entry)
		}
		if len(next) != len(current) {
			_, err := skills.SetForAgent(ctx, tx, spaceID, agentID, userID, next) // append-only; no detached files
			return err
		}
		return nil
	})
	if err != nil {
		// A lost commit acknowledgement can still mean committed. Never remove files
		// referenced by a possibly committed row; an unreferenced blob is recoverable.
		if !errors.Is(err, tenancy.ErrCommitTransaction) {
			for _, item := range prepared {
				item.kept = false
			}
		}
		cleanup()
		writeSkillError(w, r, err)
		return
	}
	cleanup()
	if items == nil {
		items = []marketApplyItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
