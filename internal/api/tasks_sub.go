package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/policy"
)

// 任务子资源接口：关注者、关联、链接、字段值、表情、时间线。

func (h *Handler) registerTaskSubRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	base := taskBase + "/{taskID}"
	mux.HandleFunc("GET "+base+"/followers", h.listTaskFollowers)
	mux.HandleFunc("PUT "+base+"/followers/{kind}/{id}", h.putTaskFollower)
	mux.HandleFunc("DELETE "+base+"/followers/{kind}/{id}", h.deleteTaskFollower)
	mux.HandleFunc("GET "+base+"/relations", h.listTaskRelations)
	mux.HandleFunc("POST "+base+"/relations", h.createTaskRelation)
	mux.HandleFunc("DELETE "+base+"/relations/{relationID}", h.deleteTaskRelation)
	mux.HandleFunc("GET "+base+"/links", h.listTaskLinks)
	mux.HandleFunc("POST "+base+"/links", h.createTaskLink)
	mux.HandleFunc("PATCH "+base+"/links/{linkID}", h.updateTaskLink)
	mux.HandleFunc("DELETE "+base+"/links/{linkID}", h.deleteTaskLink)
	mux.HandleFunc("GET "+base+"/fields", h.listTaskFields)
	mux.HandleFunc("PUT "+base+"/fields/{fieldID}", h.putTaskField)
	mux.HandleFunc("DELETE "+base+"/fields/{fieldID}", h.deleteTaskField)
	mux.HandleFunc("PUT "+base+"/reactions", h.putTaskReaction)
	mux.HandleFunc("DELETE "+base+"/reactions", h.deleteTaskReaction)
	mux.HandleFunc("PATCH "+base+"/comments/{commentID}", h.editTaskComment)
	mux.HandleFunc("DELETE "+base+"/comments/{commentID}", h.deleteTaskComment)
	mux.HandleFunc("PUT "+base+"/comments/{commentID}/resolution", h.setTaskCommentResolution)
	mux.HandleFunc("GET "+base+"/timeline", h.listTaskTimeline)
}

// taskSub 是子资源处理函数的上下文。
type taskSub struct {
	call      taskCall
	access    collaboration.SpaceAccess
	service   collaboration.Service
	spaceID   string
	taskID    string
	requestID string
}

// subResource 完成认证、路径解析与权限检查后，在租户事务里执行 fn。
func (h *Handler) subResource(w http.ResponseWriter, r *http.Request, mutation bool, action policy.Action, fn func(ctx context.Context, tx pgx.Tx, sub taskSub) error) bool {
	call, ok := h.beginTaskCall(w, r, mutation)
	if !ok {
		return false
	}
	sub := taskSub{call: call, spaceID: r.PathValue("spaceID"), taskID: r.PathValue("taskID")}
	if mutation {
		sub.requestID = collaborationRequestID(w, r)
	}
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		sub.service = service
		access, err := service.LoadSpaceAccess(ctx, tx, call.organizationID, sub.spaceID, call.selected.Actor.UserID)
		if err != nil {
			return err
		}
		sub.access = access
		if err := call.checkSpaceAccess(access, action, sub.taskID); err != nil {
			return err
		}
		return fn(ctx, tx, sub)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return false
	}
	return true
}

func (h *Handler) listTaskFollowers(w http.ResponseWriter, r *http.Request) {
	var followers []collaboration.TaskFollower
	var following bool
	if h.subResource(w, r, false, policy.ActionReadObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) (err error) {
		followers, following, err = sub.service.ListFollowers(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, sub.call.selected.Actor)
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"followers": followers, "following": following})
	}
}

// followerAction：关注或取消关注自己只需要读权限，操作他人或 Agent 需要更新权限。
func (h *Handler) followerAction(w http.ResponseWriter, r *http.Request, add bool) {
	kind, id := strings.ToLower(r.PathValue("kind")), strings.ToLower(r.PathValue("id"))
	self := kind == "human" && id == strings.ToLower(h.followerActorID(r))
	action := policy.ActionUpdateObject
	if self {
		action = policy.ActionReadObject
	}
	if h.subResource(w, r, true, action, func(ctx context.Context, tx pgx.Tx, sub taskSub) error {
		actor := sub.call.selected.Actor
		if add {
			return sub.service.AddFollower(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, actor, kind, id)
		}
		return sub.service.RemoveFollower(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, actor, kind, id)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// followerActorID 返回当前登录用户 ID（认证失败时返回空串，后续步骤会拒绝请求）。
func (h *Handler) followerActorID(r *http.Request) string {
	result, err := h.authenticate(r)
	if err != nil {
		return ""
	}
	return result.principal.UserID
}

func (h *Handler) putTaskFollower(w http.ResponseWriter, r *http.Request) {
	h.followerAction(w, r, true)
}
func (h *Handler) deleteTaskFollower(w http.ResponseWriter, r *http.Request) {
	h.followerAction(w, r, false)
}

func (h *Handler) listTaskRelations(w http.ResponseWriter, r *http.Request) {
	var relations []collaboration.TaskRelation
	if h.subResource(w, r, false, policy.ActionReadObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) (err error) {
		relations, err = sub.service.ListRelations(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, sub.call.selected.Actor)
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"relations": relations})
	}
}

func (h *Handler) createTaskRelation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TypeID       string `json:"type_id"`
		TargetTaskID string `json:"target_task_id"`
		Direction    string `json:"direction"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	var relation collaboration.TaskRelation
	replayed := false
	if h.subResource(w, r, true, policy.ActionUpdateObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) error {
		actor := sub.call.selected.Actor
		claim, err := idempotency.Begin(ctx, tx, "task_relation.create", key, struct {
			TaskID string `json:"task_id"`
			Body   any    `json:"body"`
		}{sub.taskID, input})
		if err != nil {
			return err
		}
		if claim.Replay {
			replayed = true
			relation, err = sub.service.GetRelation(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, claim.ResourceID, actor)
			return err
		}
		relation, err = sub.service.CreateRelation(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, actor,
			input.TypeID, input.TargetTaskID, input.Direction, sub.requestID)
		if err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, claim, relation.ID)
	}) {
		writeCreated(w, replayed, relation)
	}
}

func (h *Handler) deleteTaskRelation(w http.ResponseWriter, r *http.Request) {
	relationID := r.PathValue("relationID")
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	requestID := collaborationRequestID(w, r)
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		// 当前任务或关联另一端任务任一有更新权限即可删除。
		allowed := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, taskID)
		if allowed != nil {
			otherSpace, otherTask, err := service.RelationOtherEnd(ctx, tx, taskID, relationID)
			if err != nil {
				return allowed
			}
			if err := call.authorizeSpace(ctx, tx, service, otherSpace, policy.ActionUpdateObject, otherTask); err != nil {
				return allowed
			}
		}
		return service.DeleteRelation(ctx, tx, call.organizationID, spaceID, taskID, relationID, call.selected.Actor, requestID)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listTaskLinks(w http.ResponseWriter, r *http.Request) {
	var links []collaboration.TaskLink
	if h.subResource(w, r, false, policy.ActionReadObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) (err error) {
		links, err = sub.service.ListLinks(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, sub.call.selected.Actor)
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"links": links})
	}
}

func (h *Handler) createTaskLink(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	var link collaboration.TaskLink
	replayed := false
	if h.subResource(w, r, true, policy.ActionUpdateObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) error {
		actor := sub.call.selected.Actor
		claim, err := idempotency.Begin(ctx, tx, "task_link.create", key, struct {
			TaskID string `json:"task_id"`
			URL    string `json:"url"`
			Title  string `json:"title"`
		}{sub.taskID, input.URL, input.Title})
		if err != nil {
			return err
		}
		if claim.Replay {
			replayed = true
			link, err = sub.service.GetLink(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, claim.ResourceID, actor)
			return err
		}
		link, err = sub.service.CreateLink(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, actor, input.URL, input.Title, sub.requestID)
		if err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, claim, link.ID)
	}) {
		writeCreated(w, replayed, link)
	}
}

func (h *Handler) updateTaskLink(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL     *string `json:"url"`
		Title   *string `json:"title"`
		Version uint64  `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	var link collaboration.TaskLink
	if h.subResource(w, r, true, policy.ActionUpdateObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) (err error) {
		link, err = sub.service.UpdateLink(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, r.PathValue("linkID"),
			sub.call.selected.Actor, input.URL, input.Title, input.Version)
		return err
	}) {
		writeJSON(w, http.StatusOK, link)
	}
}

func (h *Handler) deleteTaskLink(w http.ResponseWriter, r *http.Request) {
	if h.subResource(w, r, true, policy.ActionUpdateObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) error {
		return sub.service.DeleteLink(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, r.PathValue("linkID"),
			sub.call.selected.Actor, sub.requestID)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) listTaskFields(w http.ResponseWriter, r *http.Request) {
	var fields []collaboration.TaskFieldValue
	if h.subResource(w, r, false, policy.ActionReadObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) (err error) {
		fields, err = sub.service.ListTaskFields(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, sub.call.selected.Actor)
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"fields": fields})
	}
}

func (h *Handler) putTaskField(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Value json.RawMessage `json:"value"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	fieldID := r.PathValue("fieldID")
	var value json.RawMessage
	var updatedAt any
	if h.subResource(w, r, true, policy.ActionUpdateObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) error {
		stored, at, err := sub.service.SetTaskField(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, fieldID,
			sub.call.selected.Actor, input.Value, sub.requestID)
		value, updatedAt = stored, at
		return err
	}) {
		if value == nil {
			value = json.RawMessage("null")
		}
		writeJSON(w, http.StatusOK, map[string]any{"field_id": fieldID, "value": value, "updated_at": updatedAt})
	}
}

func (h *Handler) deleteTaskField(w http.ResponseWriter, r *http.Request) {
	if h.subResource(w, r, true, policy.ActionUpdateObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) error {
		return sub.service.ClearTaskField(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, r.PathValue("fieldID"),
			sub.call.selected.Actor, sub.requestID)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) putTaskReaction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Emoji     string `json:"emoji"`
		CommentID string `json:"comment_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if h.subResource(w, r, true, policy.ActionCreateObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) error {
		return sub.service.AddReaction(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, sub.call.selected.Actor, input.Emoji, input.CommentID)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) deleteTaskReaction(w http.ResponseWriter, r *http.Request) {
	emoji, commentID := r.URL.Query().Get("emoji"), r.URL.Query().Get("comment_id")
	if h.subResource(w, r, true, policy.ActionCreateObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) error {
		return sub.service.RemoveReaction(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, sub.call.selected.Actor, emoji, commentID)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) listTaskTimeline(w http.ResponseWriter, r *http.Request) {
	limit, err := collaboration.ParsePageSize(r.URL.Query().Get("limit"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var cursor *collaboration.Cursor
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		parsed, err := collaboration.ParseCursor(raw)
		if err != nil {
			writeCollaborationError(w, r, err)
			return
		}
		cursor = &parsed
	}
	var page collaboration.TimelinePage
	if h.subResource(w, r, false, policy.ActionReadObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) (err error) {
		page, err = sub.service.ListTimeline(ctx, tx, sub.call.organizationID, sub.spaceID, sub.taskID, sub.call.selected.Actor, sub.call.checkSpaceAccess(sub.access, policy.ActionUpdateObject, sub.taskID) == nil, limit, cursor, r.URL.Query().Get("thread"))
		return err
	}) {
		// 新接口：没有下一页时 next_cursor 为 null。
		response := map[string]any{"items": page.Items, "next_cursor": nullableString(page.NextCursor)}
		if page.Total != nil {
			response["total"] = *page.Total
		}
		writeJSON(w, http.StatusOK, response)
	}
}
