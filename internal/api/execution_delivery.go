package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/deviceauth"
	"openfox/internal/execution"
	"openfox/internal/executiondelivery"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

const deliveryPollInterval = time.Second

// deliveryWriteTimeout 是 SSE 单次写入（含心跳）的期限；每次写入前续期。
const deliveryWriteTimeout = 30 * time.Second

func (h *Handler) registerExecutionDeliveryRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET /v1/organizations/{organizationID}/conversations/{conversationID}/runs", h.listConversationRuns)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/conversations/{conversationID}/artifacts", h.listConversationArtifacts)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runs/{runID}", h.getRun)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runs/{runID}/events", h.listRunEvents)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runs/{runID}/result", h.getRunResult)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runs/{runID}/artifacts", h.listRunArtifacts)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runs/{runID}/artifacts/{artifactID}", h.downloadArtifact)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/events", h.streamEvents)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/events", h.appendRunEvents)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/result", h.appendRunResult)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/artifacts", h.uploadArtifact)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/runtime-session-exports", h.startRuntimeSessionExport)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/conversations/{conversationID}/runtime-session-exports/{exportID}", h.getRuntimeSessionExport)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/conversations/{conversationID}/runtime-session-exports/{exportID}/content", h.downloadRuntimeSessionExport)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runtime-session-exports/{exportID}/content", h.uploadRuntimeSessionExport)
}

type deliveryEventRequest struct {
	Events []struct {
		ID         string          `json:"id"`
		Generation int64           `json:"generation"`
		Sequence   int64           `json:"sequence"`
		Type       string          `json:"type"`
		Payload    json.RawMessage `json:"safe_payload"`
		OccurredAt time.Time       `json:"occurred_at"`
	} `json:"events"`
}

type deliveryResultRequest struct {
	ID          string          `json:"id"`
	Generation  int64           `json:"generation"`
	Status      string          `json:"status"`
	Summary     string          `json:"summary"`
	Output      string          `json:"output"`
	Checks      json.RawMessage `json:"checks"`
	Unverified  bool            `json:"unverified"`
	ArtifactIDs []string        `json:"artifact_ids"`
	BranchRef   string          `json:"branch_ref"`
	PayloadHash string          `json:"payload_hash"`
	// Usage 可省略（旧 daemon）；未知字段同样被严格解码拒绝。
	Usage *executiondelivery.Usage `json:"usage"`
}

func (h *Handler) listConversationRuns(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	limit, after, ok := deliveryPage(r, true)
	if !ok {
		writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var page executiondelivery.RunPage
	var details map[string]executiondelivery.RunDetail
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		page, err = h.delivery.ListConversationRuns(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, limit, after)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(page.Items))
		for _, run := range page.Items {
			ids = append(ids, run.ID)
		}
		details, err = h.delivery.RunDetails(ctx, tx, ids)
		return err
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	items := make([]deliveryRunResponse, 0, len(page.Items))
	for _, run := range page.Items {
		items = append(items, deliveryRunResponseFrom(run, details[run.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": items, "next_cursor": page.NextCursor})
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var run execution.Run
	var detail executiondelivery.RunDetail
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		run, err = h.delivery.GetRun(ctx, tx, selection.OrganizationID, r.PathValue("runID"), actor)
		if err != nil {
			return err
		}
		detail, err = h.runDetail(ctx, tx, run.ID)
		return err
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deliveryRunResponseFrom(run, detail))
}

func (h *Handler) listRunEvents(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	limit := deliveryLimit(r)
	after, ok := deliveryCursor(r, "after")
	if limit == 0 || !ok {
		writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var page executiondelivery.EventPage
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		page, err = h.delivery.ListRunEvents(ctx, tx, selection.OrganizationID, r.PathValue("runID"), actor, after, limit)
		return err
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": page.Items, "next_cursor": page.NextCursor})
}

func (h *Handler) getRunResult(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var result executiondelivery.Result
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = h.delivery.GetResult(ctx, tx, selection.OrganizationID, r.PathValue("runID"), actor)
		return err
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) listRunArtifacts(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var items []executiondelivery.Artifact
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = h.delivery.ListArtifacts(ctx, tx, selection.OrganizationID, r.PathValue("runID"), actor)
		return err
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": items})
}

func (h *Handler) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeDeliveryError(w, r, executiondelivery.ErrStorageUnavailable)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var artifact executiondelivery.Artifact
	var key string
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		artifact, key, err = h.delivery.GetArtifactFile(ctx, tx, selection.OrganizationID, r.PathValue("runID"), r.PathValue("artifactID"), actor)
		return err
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	file, err := h.delivery.Storage.Open(r.Context(), key)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeDeliveryError(w, r, executiondelivery.ErrNotFound)
		return
	}
	defer file.Close()
	setArtifactDownloadHeaders(w.Header(), artifact)
	if _, err := io.Copy(w, file); err != nil && !errors.Is(err, context.Canceled) {
		return
	}
}

// streamEvents 是统一事件流：同一连接推送 Run 执行事件（event: run.event）与组织事件（event: <type>）。
// 游标格式与兼容规则见 executiondelivery.ParseStreamCursor；after 参数优先于 Last-Event-ID。
func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	raw := r.URL.Query().Get("after")
	if strings.TrimSpace(raw) == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	cursor, err := executiondelivery.ParseStreamCursor(raw)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
		return
	}
	// Anything committed after the client sees HTTP 200 must be visible to
	// the first poll, even if it finishes during the polling interval.
	if cursor.OrgFromWatermark {
		err = h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			cursor, err = cursor.ResolveWatermark(ctx, tx)
			return err
		})
		if err != nil {
			writeDeliveryError(w, r, err)
			return
		}
	}
	// 服务器全局 WriteTimeout（15s）对长连接不适用：只对本响应取消写超时，
	// 改为每次写入前续期，客户端卡住不读时仍会在 deliveryWriteTimeout 后断开。
	controller := http.NewResponseController(w)
	extendWrite := func() {
		_ = controller.SetWriteDeadline(time.Now().Add(deliveryWriteTimeout))
	}
	extendWrite()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	poll := time.NewTicker(deliveryPollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			extendWrite()
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-poll.C:
			extendWrite()
			var page executiondelivery.StreamPage
			err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				page, err = h.delivery.ListVisibleEvents(ctx, tx, selection.OrganizationID, actor, cursor, executiondelivery.MaxPageSize)
				return err
			})
			if err != nil {
				writeSSEError(w, err)
				flusher.Flush()
				return
			}
			for index, event := range page.RunEvents {
				if err := writeSSEFrame(w, page.RunCursors[index].String(), "run.event", event); err != nil {
					writeSSEError(w, err)
					flusher.Flush()
					return
				}
			}
			for index, event := range page.OrgEvents {
				if err := writeSSEFrame(w, page.OrgCursors[index].String(), event.Type, event); err != nil {
					writeSSEError(w, err)
					flusher.Flush()
					return
				}
			}
			cursor = page.Cursor
			if len(page.RunEvents) > 0 || len(page.OrgEvents) > 0 {
				flusher.Flush()
			}
		}
	}
}

func writeSSEFrame(w io.Writer, id, eventType string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, eventType, payload)
	return err
}

func (h *Handler) appendRunEvents(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input deliveryEventRequest
	if err := decodeDeliveryJSON(w, r, &input); err != nil {
		writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
		return
	}
	events := make([]executiondelivery.EventInput, 0, len(input.Events))
	for _, event := range input.Events {
		events = append(events, executiondelivery.EventInput{
			ID: event.ID, Generation: event.Generation, Sequence: event.Sequence,
			Type: event.Type, Payload: event.Payload, OccurredAt: event.OccurredAt,
		})
	}
	var result executiondelivery.EventBatchResult
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = h.delivery.AppendEvents(ctx, tx, selection.OrganizationID, principal, r.PathValue("runID"), events)
		return err
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) appendRunResult(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input deliveryResultRequest
	if err := decodeDeliveryJSON(w, r, &input); err != nil {
		writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
		return
	}
	var result executiondelivery.Result
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = h.delivery.AppendResult(ctx, tx, selection.OrganizationID, principal, r.PathValue("runID"), executiondelivery.ResultInput{
			ID: input.ID, Generation: input.Generation, Status: input.Status, Summary: input.Summary,
			Output: input.Output, Checks: input.Checks, Unverified: input.Unverified,
			ArtifactIDs: input.ArtifactIDs, BranchRef: input.BranchRef, PayloadHash: input.PayloadHash,
			Usage: input.Usage,
		})
		return err
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) uploadArtifact(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeDeliveryError(w, r, executiondelivery.ErrStorageUnavailable)
		return
	}
	if r.ContentLength > artifacts.MaxArtifactBytes {
		writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
		return
	}
	// 代次必填（只有发布工具会上传，旧电脑端从未调用过这个接口）；Run 已结束或代次不符 409。
	generation, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("X-Run-Generation")), 10, 64)
	if err != nil || generation < 1 {
		writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
		return
	}
	artifactID := strings.TrimSpace(r.Header.Get("X-Artifact-ID"))
	var uploadLimit int64
	err = h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := h.delivery.CheckArtifactUpload(ctx, tx, principal, r.PathValue("runID"), generation, artifactID); err != nil {
			return err
		}
		var err error
		requestedBytes := int64(0)
		if r.ContentLength >= 0 {
			requestedBytes = r.ContentLength
		}
		uploadLimit, err = h.delivery.ArtifactUploadLimit(ctx, tx, selection.OrganizationID, r.PathValue("runID"), requestedBytes)
		return err
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if artifactID == "" {
		var err error
		artifactID, err = newUUID()
		if err != nil {
			writeDeliveryError(w, r, err)
			return
		}
	}
	// 文件名只取最后一个路径成分并清理控制字符；MIME 只认可预览白名单（其余记为 application/octet-stream）。
	name := executiondelivery.CleanArtifactName(r.Header.Get("X-Artifact-Name"))
	mimeType := strings.TrimSpace(r.Header.Get("X-Artifact-MIME"))
	if parsed, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = parsed
	}
	mimeType = executiondelivery.NormalizeArtifactMIME(mimeType)
	var retentionUntil *time.Time
	if raw := strings.TrimSpace(r.Header.Get("X-Artifact-Retention-Until")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil || !value.After(time.Now().UTC()) {
			writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
			return
		}
		retentionUntil = &value
	}
	storageKey := fmt.Sprintf("%s/%s/%s", selection.OrganizationID, r.PathValue("runID"), artifactID)
	stored, err := h.delivery.Storage.Put(r.Context(), storageKey, r.Body, uploadLimit)
	if err != nil {
		writeArtifactStorageError(w, r, err)
		return
	}
	var artifact executiondelivery.Artifact
	created := false
	err = h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		artifact, created, err = h.delivery.AddArtifact(ctx, tx, selection.OrganizationID, principal, r.PathValue("runID"), generation, artifactID, name, storageKey, mimeType, stored, retentionUntil)
		return err
	})
	if err != nil {
		_ = h.delivery.Storage.Delete(context.Background(), storageKey)
		writeDeliveryError(w, r, err)
		return
	}
	if !created {
		// 同一 Run 已有内容相同的产物：删除这次写入的对象，返回已有产物。
		_ = h.delivery.Storage.Delete(context.Background(), storageKey)
		writeJSON(w, http.StatusOK, artifact)
		return
	}
	writeJSON(w, http.StatusCreated, artifact)
}

// listConversationArtifacts 返回会话产物列表：能读会话即可读，读不到 404。
func (h *Handler) listConversationArtifacts(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var items []executiondelivery.Artifact
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = h.delivery.ListConversationArtifacts(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor)
		return err
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": items})
}

// setArtifactDownloadHeaders 设置产物下载的响应头：任何类型都按附件下载、不让浏览器嗅探类型，
// 并加 CSP sandbox，即使被直接打开也不会执行脚本（SVG、HTML 等）；内容可能敏感，不缓存。
func setArtifactDownloadHeaders(header http.Header, artifact executiondelivery.Artifact) {
	header.Set("Content-Type", artifact.MIMEType)
	header.Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "sandbox")
	header.Set("Cache-Control", "private, no-store")
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Name})
	if disposition == "" {
		disposition = "attachment"
	}
	header.Set("Content-Disposition", disposition)
}

func (h *Handler) deliveryDeviceAuth(w http.ResponseWriter, r *http.Request) (deviceauth.Principal, tenancy.OrganizationSelection, bool) {
	token := bearerToken(r)
	if token == "" {
		writeDeliveryError(w, r, deviceauth.ErrSessionNotFound)
		return deviceauth.Principal{}, tenancy.OrganizationSelection{}, false
	}
	principal, err := h.deviceAuth.Authenticate(r.Context(), token)
	if err != nil {
		writeDeliveryError(w, r, err)
		return deviceauth.Principal{}, tenancy.OrganizationSelection{}, false
	}
	selection, err := h.deviceAuth.SelectOrganization(r.Context(), principal.DeviceID, r.PathValue("organizationID"))
	if err != nil {
		writeDeviceSelectionError(w, r, err)
		return deviceauth.Principal{}, tenancy.OrganizationSelection{}, false
	}
	return principal, selection, true
}

func (h *Handler) runDeliveryUserTransaction(r *http.Request, selection tenancy.OrganizationSelection, fn func(context.Context, pgx.Tx) error) error {
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
	return h.pool.RunForOrganization(r.Context(), selection.OrganizationID, tenancy.SourceUserRequest, selector, fn)
}

func (h *Handler) runDeliveryDeviceTransaction(r *http.Request, selection tenancy.OrganizationSelection, fn func(context.Context, pgx.Tx) error) error {
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
	return h.pool.RunForOrganization(r.Context(), selection.OrganizationID, tenancy.SourceDeviceRequest, selector, fn)
}

func deliveryPage(r *http.Request, timeCursor bool) (int, time.Time, bool) {
	limit := executiondelivery.MaxPageSize
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > executiondelivery.MaxPageSize {
			return 0, time.Time{}, false
		}
		limit = parsed
	}
	if timeCursor {
		cursor, ok := deliveryCursor(r, "after")
		if !ok {
			return 0, time.Time{}, false
		}
		if cursor == 0 {
			return limit, time.Time{}, true
		}
		return limit, time.Unix(0, cursor).UTC(), true
	}
	return limit, time.Time{}, true
}

func deliveryLimit(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return executiondelivery.MaxPageSize
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > executiondelivery.MaxPageSize {
		return 0
	}
	return value
}

func deliveryCursor(r *http.Request, name string) (int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" || raw == "0" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value >= 0
}

func decodeDeliveryJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
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

type deliveryRunResponse struct {
	ID                     string     `json:"id"`
	OrganizationID         string     `json:"organization_id"`
	SpaceID                string     `json:"space_id"`
	ConversationID         string     `json:"conversation_id"`
	TaskID                 string     `json:"task_id,omitempty"`
	AgentID                string     `json:"agent_id"`
	ComputerID             string     `json:"computer_id"`
	GrantID                string     `json:"grant_id"`
	ConfigVersion          int64      `json:"config_version"`
	GrantVersion           int64      `json:"grant_version"`
	ActorID                string     `json:"actor_id"`
	State                  string     `json:"state"`
	Generation             int64      `json:"generation"`
	ReconciliationRequired bool       `json:"reconciliation_required"`
	RetryOf                string     `json:"retry_of,omitempty"`
	Reason                 string     `json:"reason"`
	WaitReason             string     `json:"wait_reason,omitempty"`
	ReservationID          string     `json:"reservation_id,omitempty"`
	OfferID                string     `json:"offer_id,omitempty"`
	Version                int64      `json:"version"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	CancelRequestedAt      *time.Time `json:"cancel_requested_at,omitempty"`
	FinishedAt             *time.Time `json:"finished_at,omitempty"`
	// 始终输出（可为 null），见契约 RunResponse。
	FailureCode string                   `json:"failure_code"`
	StartedAt   *time.Time               `json:"started_at"`
	Usage       *executiondelivery.Usage `json:"usage"`
	DurationMS  *int64                   `json:"duration_ms"`
}

// runDetail 读取单个 Run 的补充事实；Run 已在同一事务内确认存在且可见。
func (h *Handler) runDetail(ctx context.Context, tx pgx.Tx, runID string) (executiondelivery.RunDetail, error) {
	details, err := h.delivery.RunDetails(ctx, tx, []string{runID})
	return details[runID], err
}

func deliveryRunResponseFrom(run execution.Run, detail executiondelivery.RunDetail) deliveryRunResponse {
	var duration *int64
	if detail.StartedAt != nil && run.FinishedAt != nil {
		value := run.FinishedAt.Sub(*detail.StartedAt).Milliseconds()
		if value < 0 {
			value = 0
		}
		duration = &value
	}
	return deliveryRunResponse{
		FailureCode: detail.FailureCode, StartedAt: detail.StartedAt, Usage: detail.Usage, DurationMS: duration,
		ID: run.ID, OrganizationID: run.OrganizationID, SpaceID: run.SpaceID,
		ConversationID: run.ConversationID, TaskID: run.TaskID, AgentID: run.AgentID,
		ComputerID: run.ComputerID, GrantID: run.GrantID, ConfigVersion: run.ConfigVersion,
		GrantVersion: run.GrantVersion, ActorID: run.ActorID, State: string(run.State),
		Generation: run.Generation, ReconciliationRequired: run.ReconciliationRequired,
		RetryOf: run.RetryOf, Reason: run.Reason, WaitReason: string(run.WaitReason),
		ReservationID: run.ReservationID, OfferID: run.OfferID, Version: run.Version,
		CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		CancelRequestedAt: run.CancelRequestedAt, FinishedAt: run.FinishedAt,
	}
}

func writeArtifactStorageError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, artifacts.ErrTooLarge) || errors.Is(err, artifacts.ErrInvalidKey) {
		writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
		return
	}
	writeDeliveryError(w, r, executiondelivery.ErrStorageUnavailable)
}

func writeSSEError(w http.ResponseWriter, err error) {
	code := "INTERNAL_ERROR"
	if errors.Is(err, executiondelivery.ErrForbidden) {
		code = "FORBIDDEN"
	} else if errors.Is(err, executiondelivery.ErrNotFound) {
		code = "NOT_FOUND"
	}
	_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", sseJSON(map[string]any{"code": code}))
}

func sseJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func writeDeliveryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrSessionNotFound), errors.Is(err, deviceauth.ErrSessionNotFound), errors.Is(err, deviceauth.ErrDeviceUnauthenticated):
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
	case errors.Is(err, executiondelivery.ErrInvalidRequest):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request is invalid")
	case errors.Is(err, executiondelivery.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "resource is not available")
	case errors.Is(err, executiondelivery.ErrEventConflict), errors.Is(err, executiondelivery.ErrResultConflict), errors.Is(err, executiondelivery.ErrGenerationConflict):
		writeError(w, r, http.StatusConflict, "DELIVERY_CONFLICT", "delivery conflicts with the current Run state")
	case errors.Is(err, executiondelivery.ErrResourceLimit):
		writeError(w, r, http.StatusRequestEntityTooLarge, "RESOURCE_LIMIT", "the configured artifact limit was reached")
	case errors.Is(err, executiondelivery.ErrReconciliationRequired):
		writeError(w, r, http.StatusConflict, "RECONCILIATION_REQUIRED", "the Run requires process reconciliation before result acceptance")
	case errors.Is(err, executiondelivery.ErrCursorExpired):
		writeError(w, r, http.StatusGone, "CURSOR_EXPIRED", "event cursor is no longer available")
	case errors.Is(err, executiondelivery.ErrStorageUnavailable), errors.Is(err, artifacts.ErrNoRoot), errors.Is(err, postgres.ErrNilPool):
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "storage or database is not ready")
	case errors.Is(err, executiondelivery.ErrNotFound), errors.Is(err, postgres.ErrOrganizationNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}
