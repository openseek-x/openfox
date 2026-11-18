package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/integrations"
	"openfox/internal/mcpconnect"
	"openfox/internal/mcpoauth"
	"openfox/internal/membership"
	"openfox/internal/secrets"
)

const mcpCallback = "/api/v1/mcp/oauth/callback"

type attemptPayload struct {
	Session    mcpoauth.Pending
	Version    int64
	InstanceID string
}

func (h *Handler) registerMCPOAuthRoutes(mux *http.ServeMux) {
	const path = "/v1/organizations/{organizationID}/spaces/{spaceID}/agents/{agentID}/mcp-servers/{serverID}/oauth"
	mux.HandleFunc("POST "+path+"/start", h.startMCPOAuth)
	mux.HandleFunc("GET "+path+"/status", h.statusMCPOAuth)
	mux.HandleFunc("GET /v1/mcp/oauth/wait", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "正在打开服务商的授权页面…")
	})
	mux.HandleFunc("GET /v1/mcp/oauth/callback", h.finishMCPOAuth)
}
func (h *Handler) startMCPOAuth(w http.ResponseWriter, r *http.Request) {
	a, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeAgentError(w, r, agents.ErrInvalidRequest)
		return
	}
	if h.secrets == nil {
		writeAgentError(w, r, agents.ErrSecretsUnavailable)
		return
	}
	space, agent, server := r.PathValue("spaceID"), r.PathValue("agentID"), r.PathValue("serverID")
	s, err := h.agentStore().MCPForOAuth(r.Context(), a.Selection, h.agentActor(a, r), space, agent, server)
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	if s.Config.Scope != mcpconnect.Public || s.Config.AuthMode != "oauth" {
		writeAgentError(w, r, agents.ErrInvalidConfig)
		return
	}
	state := strings.Join([]string{a.Selection.OrganizationID, space, agent, server, mcpoauth.Random()}, ".")
	ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
	defer cancel()
	p, destination, err := mcpoauth.New(h.importSafe).Start(ctx, s.Config.URL, h.centerURL()+mcpCallback, state, input.ClientID, input.ClientSecret)
	if err != nil {
		writeError(w, r, 422, "MCP_OAUTH_DISCOVERY", "服务未提供兼容的 OAuth 配置，请检查地址；若服务要求预注册，请填写该服务的客户端 ID。")
		return
	}
	raw, _ := json.Marshal(attemptPayload{p, int64(s.Version), s.InstanceID})
	sealed, err := h.secrets.Seal(raw, "mcp-oauth:"+state+":"+a.Actor.UserID)
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	blob, _ := json.Marshal(sealed)
	err = h.runDeliveryUserTransaction(r, a.Selection, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO openfox.agent_mcp_oauth_attempts(organization_id,user_id,agent_id,server_id,state_hash,expires_at,pending,status) VALUES ($1,$2,$3,$4,$5,now()+interval '10 minutes',$6,'pending') ON CONFLICT (organization_id,user_id,agent_id,server_id) DO UPDATE SET state_hash=excluded.state_hash,expires_at=excluded.expires_at,pending=excluded.pending,status='pending'`, a.Selection.OrganizationID, a.Actor.UserID, agent, server, integrations.TokenHash(state), blob)
		return e
	})
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"authorize_url": destination})
}
func (h *Handler) statusMCPOAuth(w http.ResponseWriter, r *http.Request) {
	a, ok := h.beginAgentRequest(w, r, false)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if _, err := h.agentStore().MCPForOAuth(r.Context(), a.Selection, h.agentActor(a, r), r.PathValue("spaceID"), r.PathValue("agentID"), r.PathValue("serverID")); err != nil {
		writeAgentError(w, r, err)
		return
	}
	var status string
	err := h.runDeliveryUserTransaction(r, a.Selection, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT CASE WHEN expires_at < now() AND status IN ('pending','exchanging') THEN 'expired' ELSE status END FROM openfox.agent_mcp_oauth_attempts WHERE organization_id=$1 AND user_id=$2 AND agent_id=$3 AND server_id=$4`, a.Selection.OrganizationID, a.Actor.UserID, r.PathValue("agentID"), r.PathValue("serverID")).Scan(&status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		status = "missing"
	} else if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": status})
}
func (h *Handler) finishMCPOAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fail := func() {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, "授权未完成，请返回 OpenFox 重新连接。")
	}
	q := r.URL.Query()
	state := q.Get("state")
	parts := strings.Split(state, ".")
	if len(parts) != 5 || len(parts[4]) != 43 || h.secrets == nil {
		fail()
		return
	}
	for i, k := range []string{"organizationID", "spaceID", "agentID", "serverID"} {
		if !membership.ValidateUUID(parts[i]) {
			fail()
			return
		}
		r.SetPathValue(k, parts[i])
	}
	a, ok := h.beginAgentRequest(w, r, false)
	if !ok {
		return
	}
	hash := integrations.TokenHash(state)
	var blob []byte
	err := h.runDeliveryUserTransaction(r, a.Selection, func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT pending FROM openfox.agent_mcp_oauth_attempts WHERE organization_id=$1 AND user_id=$2 AND agent_id=$3 AND server_id=$4 AND state_hash=$5 AND status='pending' AND expires_at>now() FOR UPDATE`, parts[0], a.Actor.UserID, parts[2], parts[3], hash).Scan(&blob); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.agent_mcp_oauth_attempts SET status='exchanging',pending=NULL WHERE state_hash=$1 AND organization_id=$2 AND user_id=$3 AND agent_id=$4 AND server_id=$5`, hash, parts[0], a.Actor.UserID, parts[2], parts[3])
		return e
	})
	if err != nil {
		fail()
		return
	}
	status := "failed"
	defer func() {
		_ = h.runDeliveryUserTransaction(r, a.Selection, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE openfox.agent_mcp_oauth_attempts SET status=$1 WHERE state_hash=$2 AND organization_id=$3 AND user_id=$4 AND agent_id=$5 AND server_id=$6`, status, hash, parts[0], a.Actor.UserID, parts[2], parts[3])
			return e
		})
	}()
	var sealed secrets.Sealed
	var p attemptPayload
	if q.Get("error") != "" || json.Unmarshal(blob, &sealed) != nil {
		fail()
		return
	}
	raw, e := h.secrets.Open(sealed, "mcp-oauth:"+state+":"+a.Actor.UserID)
	if e != nil || json.Unmarshal(raw, &p) != nil {
		fail()
		return
	}
	store := h.agentStore()
	actor := h.agentActor(a, r)
	active, e := store.MCPForOAuth(r.Context(), a.Selection, actor, parts[1], parts[2], parts[3])
	if e != nil || active.InstanceID != p.InstanceID || int64(active.Version) != p.Version || active.Config.URL != p.Session.Resource {
		fail()
		return
	}
	config, e := store.MCPDiscoveryConfig(r.Context(), a.Selection, actor, parts[1], parts[2], parts[3], p.Version)
	if e != nil || config.URL != p.Session.Resource {
		fail()
		return
	}
	token, e := mcpoauth.New(h.importSafe).Exchange(r.Context(), p.Session, q.Get("code"), q.Get("iss"))
	if e != nil {
		fail()
		return
	}
	config.Credential = token.AccessToken
	manifest, e := mcpconnect.Discover(r.Context(), h.importSafe, config)
	if e != nil {
		fail()
		return
	}
	var expiry time.Time
	if token.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	_, e = store.BindMCPOAuth(r.Context(), a.Selection, actor, parts[1], parts[2], parts[3], p.Version, p.InstanceID, token.AccessToken, expiry, manifest)
	if e != nil {
		fail()
		return
	}
	status = "connected"
	_, _ = io.WriteString(w, "已连接。可以关闭此窗口，返回 OpenFox。")
}
