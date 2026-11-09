package api

// 第三方账号绑定页：汇总与个人 Jira 绑定。
// Jira 令牌只在内存里用一次（调 /myself 校验），不写库、不写日志、不进审计、错误信息里不带。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/platform/safehttp"
)

// jiraBindingTimeout 是校验个人 Jira 凭证的总时长上限。
const jiraBindingTimeout = 10 * time.Second

func (h *Handler) registerAccountBindingRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/me/account-bindings"
	mux.HandleFunc("GET "+base, h.listMyAccountBindings)
	mux.HandleFunc("PUT "+base+"/jira", h.putMyJiraBinding)
}

func (h *Handler) listMyAccountBindings(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var view imports.AccountBindingsView
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		view, err = (imports.Service{}).ListAccountBindings(ctx, tx, call.organizationID, call.selected.Actor, h.integrations.GitHubConfigured, h.integrations.GiteeConfigured, h.integrations.WeChatConfigured())
		return err
	})
	if err != nil {
		writeAccountBindingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type putJiraBindingRequest struct {
	SiteURL    string `json:"site_url"`
	Deployment string `json:"deployment"`
	Email      string `json:"email"`
	APIToken   string `json:"api_token"`
}

func (h *Handler) putMyJiraBinding(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input putJiraBindingRequest
	if err := decodeJSONLimit(w, r, 8<<10, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	input.Email = strings.TrimSpace(input.Email)
	if len(input.SiteURL) > 2048 || input.APIToken == "" || len(input.APIToken) > 512 || len(input.Email) > 320 ||
		(input.Deployment != jiraapi.DeploymentCloud && input.Deployment != jiraapi.DeploymentServer) ||
		(input.Deployment == jiraapi.DeploymentCloud && !strings.Contains(input.Email, "@")) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	service := imports.Service{}
	var target imports.JiraTarget
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		target, err = service.PrepareJiraBinding(ctx, tx, call.organizationID, call.selected.Actor, input.SiteURL, input.Deployment)
		return err
	}); err != nil {
		writeAccountBindingError(w, r, err)
		return
	}
	// Cloud 的邮箱必须与 OpenFox 邮箱一致，先于任何网络请求检查。
	if input.Deployment == jiraapi.DeploymentCloud && !imports.EmailMatches(input.Email, target.Email) {
		writeError(w, r, http.StatusUnprocessableEntity, "JIRA_EMAIL_MISMATCH", "the Jira email must match your OpenFox email")
		return
	}
	user, err := h.verifyMyJira(r.Context(), target, input)
	input.APIToken = "" // 用完即丢弃
	if err != nil {
		writeJiraBindingVerifyError(w, r, err)
		return
	}
	if user.EmailAddress != "" && !imports.EmailMatches(user.EmailAddress, target.Email) {
		writeError(w, r, http.StatusUnprocessableEntity, "JIRA_EMAIL_MISMATCH", "the Jira email must match your OpenFox email")
		return
	}
	var result imports.JiraBindingResult
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = service.SaveJiraBinding(ctx, tx, call.organizationID, call.selected.Actor, target, user)
		return err
	}); err != nil {
		writeAccountBindingError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// verifyMyJira 用凭证调 /rest/api/2/myself（经 SSRF 防护），返回 Jira 用户。
func (h *Handler) verifyMyJira(parent context.Context, target imports.JiraTarget, input putJiraBindingRequest) (jiraapi.User, error) {
	ctx, cancel := context.WithTimeout(parent, jiraBindingTimeout)
	defer cancel()
	auth := jiraapi.Auth{Kind: jiraapi.AuthPAT, Secret: input.APIToken}
	if target.Deployment == jiraapi.DeploymentCloud {
		auth = jiraapi.Auth{Kind: jiraapi.AuthAPIToken, Email: input.Email, Secret: input.APIToken}
	}
	client, err := jiraapi.New(jiraapi.Config{Deployment: target.Deployment, SiteURL: target.SiteURL, Auth: auth}, safehttp.Client(h.importSafe))
	if err != nil {
		return jiraapi.User{}, err
	}
	user, err := client.Myself(ctx)
	if err != nil {
		return jiraapi.User{}, err
	}
	if strings.TrimSpace(user.Identity()) == "" {
		return jiraapi.User{}, &jiraapi.Error{Reason: jiraapi.ReasonAuthFailed, Message: "无法确定 Jira 账号"}
	}
	return user, nil
}

// writeJiraBindingVerifyError 把 /myself 校验失败映射为 422 / 400 / 502（不带凭据与上游细节）。
func writeJiraBindingVerifyError(w http.ResponseWriter, r *http.Request, err error) {
	switch jiraapi.ReasonOf(err) {
	case jiraapi.ReasonAuthFailed:
		writeError(w, r, http.StatusUnprocessableEntity, "JIRA_AUTH_FAILED", "Jira rejected the credentials")
	case jiraapi.ReasonBlockedAddress:
		writeError(w, r, http.StatusBadRequest, "JIRA_BLOCKED_ADDRESS", "this Jira address is not allowed")
	case jiraapi.ReasonInvalidURL:
		writeError(w, r, http.StatusBadRequest, "JIRA_SITE_UNKNOWN", "the Jira site is not one of the organization's imported sites")
	default:
		writeError(w, r, http.StatusBadGateway, "JIRA_UNAVAILABLE", "Jira is temporarily unavailable")
	}
}

func writeAccountBindingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, imports.ErrJiraSiteUnknown):
		writeError(w, r, http.StatusBadRequest, "JIRA_SITE_UNKNOWN", "the Jira site is not one of the organization's imported sites")
	case errors.Is(err, imports.ErrJiraAccountInUse):
		writeError(w, r, http.StatusConflict, "JIRA_ACCOUNT_IN_USE", "this Jira account is already bound to another member")
	default:
		writeImportError(w, r, err)
	}
}
