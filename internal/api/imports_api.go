package api

// Jira API 导入的创建、新建空间校验与外部账号接口。

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/integrations"
	"openfox/internal/membership"
	"openfox/internal/platform/safehttp"
	"openfox/internal/policy"
)

const (
	// jiraVerifyTimeout 是创建 API 导入时同步校验连接的总时长上限。
	jiraVerifyTimeout = 30 * time.Second
	// jiraMaxIssues 是单次导入的条目上限。
	jiraMaxIssues       = 5000
	jiraAPIRequestLimit = 16 << 10
)

var projectKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,19}$`)

type jiraAPIAuth struct {
	Kind     string `json:"kind"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

type createJiraAPIImportRequest struct {
	Deployment string            `json:"deployment"`
	SiteURL    string            `json:"site_url"`
	Auth       jiraAPIAuth       `json:"auth"`
	ProjectKey string            `json:"project_key"`
	JQL        string            `json:"jql"`
	SpaceID    string            `json:"space_id"`
	NewSpace   *imports.NewSpace `json:"new_space"`
}

// authorizeImportSpace 目标空间必须存在且当前成员可写（对象类授权需要对象 ID：先生成一个，只用于授权判断）。
func authorizeImportSpace(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, spaceID string) error {
	objectID, err := membership.NewID()
	if err != nil {
		return err
	}
	if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionCreateObject, objectID); err != nil {
		if errors.Is(err, collaboration.ErrSpaceNotFound) {
			return imports.ErrSpaceNotFound
		}
		return err
	}
	return nil
}

// checkNewSpacePrefix 新建空间的前缀（如已给）必须未被占用；占用返回 IDENTIFIER_TAKEN。
func checkNewSpacePrefix(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, prefix string) error {
	if prefix == "" {
		return nil
	}
	check, err := service.CheckIdentifier(ctx, tx, call.organizationID, call.selected.Actor, prefix, "")
	if err != nil {
		return err
	}
	if !check.Valid {
		return collaboration.ErrInvalidIdentifier
	}
	if !check.Available {
		return collaboration.ErrIdentifierTaken
	}
	return nil
}

func (r createJiraAPIImportRequest) validate() error {
	invalid := func(msg string) error { return imports.ErrInvalidRequestWith(msg) }
	switch r.Deployment {
	case jiraapi.DeploymentCloud:
		if r.Auth.Kind != jiraapi.AuthAPIToken || !strings.Contains(r.Auth.Email, "@") || len(r.Auth.Email) > 320 {
			return invalid("Jira Cloud 需要邮箱与 API 令牌")
		}
	case jiraapi.DeploymentServer:
		switch r.Auth.Kind {
		case jiraapi.AuthPAT:
		case jiraapi.AuthPassword:
			if strings.TrimSpace(r.Auth.Username) == "" || len(r.Auth.Username) > 255 {
				return invalid("用户名加密码认证需要用户名")
			}
		default:
			return invalid("私有化部署的认证方式是个人访问令牌或用户名加密码")
		}
	default:
		return invalid("deployment 只能是 cloud 或 server")
	}
	if r.Auth.Secret == "" || len(r.Auth.Secret) > 2048 {
		return invalid("缺少凭据")
	}
	if len(r.SiteURL) > 255 || !projectKeyPattern.MatchString(r.ProjectKey) || len(r.JQL) > 2000 {
		return invalid("站点地址、项目 Key 或 JQL 不合法")
	}
	if (r.SpaceID == "") == (r.NewSpace == nil) {
		return invalid("space_id 与 new_space 必须二选一")
	}
	return nil
}

func (h *Handler) createJiraAPIImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if !imports.CanManage(call.selected.Actor.Role) {
		writeImportError(w, r, imports.ErrForbidden)
		return
	}
	var input createJiraAPIImportRequest
	if err := decodeJSONLimit(w, r, jiraAPIRequestLimit, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if err := input.validate(); err != nil {
		writeImportError(w, r, err)
		return
	}
	input.SpaceID = strings.ToLower(strings.TrimSpace(input.SpaceID))
	var newSpace *imports.NewSpace
	if input.NewSpace != nil {
		ns, err := imports.NormalizeNewSpace(*input.NewSpace, input.ProjectKey)
		if err != nil {
			writeImportError(w, r, err)
			return
		}
		newSpace = &ns
	}
	if h.secrets == nil {
		writeError(w, r, http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", "server encryption key is not configured")
		return
	}
	// 先校验目标（空间可写 / 前缀可用），避免对无效请求白白访问 Jira。
	checkTarget := func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if newSpace != nil {
			return checkNewSpacePrefix(ctx, tx, service, call, newSpace.Prefix)
		}
		return authorizeImportSpace(ctx, tx, service, call, input.SpaceID)
	}
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		return checkTarget(ctx, tx, service)
	}); err != nil {
		writeImportError(w, r, err)
		return
	}
	// 事务外同步校验连接（总时长 ≤ 30 秒）。
	apiInfo, total, err := h.verifyJira(r.Context(), input)
	if err != nil {
		writeJiraConnectionError(w, r, err)
		return
	}
	var job imports.Job
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := checkTarget(ctx, tx, service); err != nil {
			return err
		}
		var err error
		job, err = (imports.Service{}).CreateAPIDraft(ctx, tx, call.organizationID, call.selected.Actor, imports.APIDraftInput{
			API: apiInfo, SpaceID: input.SpaceID, NewSpace: newSpace, Total: &total, Box: h.secrets,
			Credentials: imports.Credentials{Kind: input.Auth.Kind, Email: strings.TrimSpace(input.Auth.Email),
				Username: strings.TrimSpace(input.Auth.Username), Secret: input.Auth.Secret},
		})
		if err == nil && apiInfo.AccountIdentity != "" {
			job.SyncTicket, err = h.integrations.ImportSiteTicket(call.organizationID, job.ID, call.selected.Actor.UserID, integrations.SiteGrant{Provider: "jira", Site: apiInfo.SiteURL, Identity: apiInfo.AccountIdentity, Jira: &jiraapi.Auth{Kind: input.Auth.Kind, Email: strings.TrimSpace(input.Auth.Email), Username: strings.TrimSpace(input.Auth.Username), Secret: input.Auth.Secret}})
		}
		return err
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, job)
}

// verifyJira 依次校验凭证、项目、字段、JQL 与条目数。失败返回带原因的 *jiraapi.Error。
func (h *Handler) verifyJira(parent context.Context, input createJiraAPIImportRequest) (imports.APIInfo, int, error) {
	ctx, cancel := context.WithTimeout(parent, jiraVerifyTimeout)
	defer cancel()
	client, err := jiraapi.New(jiraapi.Config{Deployment: input.Deployment, SiteURL: input.SiteURL,
		Auth: jiraapi.Auth{Kind: input.Auth.Kind, Email: strings.TrimSpace(input.Auth.Email),
			Username: strings.TrimSpace(input.Auth.Username), Secret: input.Auth.Secret}},
		safehttp.Client(h.importSafe))
	if err != nil {
		return imports.APIInfo{}, 0, err
	}
	verified, err := client.Myself(ctx)
	if err != nil {
		return imports.APIInfo{}, 0, err
	}
	project, err := client.Project(ctx, input.ProjectKey)
	if err != nil {
		return imports.APIInfo{}, 0, err
	}
	key := project.Key
	if key == "" {
		key = strings.ToUpper(input.ProjectKey)
	}
	fields, err := client.Fields(ctx)
	if err != nil {
		return imports.APIInfo{}, 0, err
	}
	ids := jiraapi.DiscoverFields(fields)
	jql := imports.ProjectJQL(key, input.JQL)
	if _, err := client.Search(ctx, jql, jiraapi.Cursor{}, nil, 1); err != nil {
		return imports.APIInfo{}, 0, err
	}
	total, err := client.Count(ctx, jql)
	if err != nil {
		return imports.APIInfo{}, 0, err
	}
	if total > jiraMaxIssues {
		return imports.APIInfo{}, 0, &jiraapi.Error{Reason: jiraapi.ReasonTooManyIssues, Message: "条目超过 5000 条"}
	}
	// auth_user 是邮箱或用户名（个人访问令牌没有），不是凭证。
	authUser := strings.TrimSpace(input.Auth.Email)
	if input.Auth.Kind == jiraapi.AuthPassword {
		authUser = strings.TrimSpace(input.Auth.Username)
	} else if input.Auth.Kind == jiraapi.AuthPAT {
		authUser = ""
	}
	return imports.APIInfo{AccountIdentity: verified.Identity(), Deployment: input.Deployment, SiteURL: client.SiteURL(), ProjectKey: key, JQL: strings.TrimSpace(input.JQL),
		AuthKind: input.Auth.Kind, AuthUser: authUser,
		FieldIDs: imports.FieldIDs{Sprint: ids.Sprint, Points: ids.Points, Start: ids.Start, EpicLink: ids.EpicLink}}, total, nil
}

// writeJiraConnectionError 把连接校验失败写成 400 JIRA_CONNECTION_FAILED（details.reason / details.status）。
func writeJiraConnectionError(w http.ResponseWriter, r *http.Request, err error) {
	reason, status := jiraapi.ReasonUnreachable, 0
	var apiErr *jiraapi.Error
	if errors.As(err, &apiErr) {
		reason, status = apiErr.Reason, apiErr.Status
	}
	details := map[string]any{"reason": reason}
	if status != 0 {
		details["status"] = status
	}
	writeErrorDetails(w, r, http.StatusBadRequest, "JIRA_CONNECTION_FAILED", "cannot connect to Jira: "+reason, details)
}

func (h *Handler) listExternalAccounts(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	query := r.URL.Query()
	params := imports.ListAccountsParams{Q: query.Get("q"), Provider: query.Get("provider"), Source: query.Get("source"),
		Link: query.Get("link"), Cursor: query.Get("cursor")}
	if len(params.Cursor) > 200 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "cursor is invalid")
		return
	}
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "limit is invalid")
			return
		}
		params.Limit = n
	}
	var result imports.AccountList
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = (imports.Service{}).ListAccounts(ctx, tx, call.organizationID, call.selected.Actor, params)
		return err
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type saveExternalAccountLinksRequest struct {
	Changes []struct {
		ID      string  `json:"id"`
		Version int64   `json:"version"`
		UserID  *string `json:"user_id"`
	} `json:"changes"`
}

func (h *Handler) saveExternalAccountLinks(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input saveExternalAccountLinksRequest
	if err := decodeJSONLimit(w, r, 64<<10, &input); err != nil || len(input.Changes) == 0 || len(input.Changes) > 200 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	changes := make([]imports.LinkChange, len(input.Changes))
	for i, c := range input.Changes {
		changes[i] = imports.LinkChange{ID: c.ID, Version: c.Version, UserID: c.UserID}
	}
	var result imports.SaveLinksResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = (imports.Service{}).SaveLinks(ctx, tx, call.organizationID, call.selected.Actor, changes)
		return err
	})
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
