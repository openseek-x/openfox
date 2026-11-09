package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/membership"
	"openfox/internal/platform/safehttp"
	"openfox/internal/secrets"
)

// API 导入的拉取参数。
const (
	maxFetchPages      = 5                // 每轮最多拉取的页数
	maxFetchFailures   = 3                // 连续失败达到次数后丢弃草稿
	maxActiveFetches   = 3                // 每个组织同时拉取中的草稿数
	pageRequestTimeout = 45 * time.Second // 一页（含评论补拉）的整体超时
	defaultFetchRetry  = 30 * time.Second // 失败后的退避基数
	defaultRateRetry   = 30               // 限流时来源没给 Retry-After 的等待秒数
)

// ErrSecretsUnavailable 表示服务端没有配置加密密钥，或存储的凭据无法解密。
var ErrSecretsUnavailable = errors.New("imports: secrets unavailable")

// Credentials 是 API 导入的认证信息（只在创建时提交一次，加密保存，拉取结束即清除）。
type Credentials struct {
	Kind     string `json:"kind"`
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret"`
}

func credentialsAAD(organizationID, jobID string) string {
	return "import:" + organizationID + ":" + jobID
}

func sealCredentials(box *secrets.Box, organizationID, jobID string, creds Credentials) (blob []byte, keyID string, err error) {
	if box == nil {
		return nil, "", ErrSecretsUnavailable
	}
	plain, err := json.Marshal(creds)
	if err != nil {
		return nil, "", err
	}
	sealed, err := box.Seal(plain, credentialsAAD(organizationID, jobID))
	if err != nil {
		return nil, "", err
	}
	return append(append([]byte{}, sealed.Nonce...), sealed.Ciphertext...), sealed.KeyID, nil
}

func openCredentials(box *secrets.Box, organizationID, jobID string, blob []byte, keyID string) (Credentials, error) {
	var creds Credentials
	if box == nil || len(blob) <= secrets.NonceSize {
		return creds, ErrSecretsUnavailable
	}
	plain, err := box.Open(secrets.Sealed{Nonce: blob[:secrets.NonceSize], Ciphertext: blob[secrets.NonceSize:], KeyID: keyID},
		credentialsAAD(organizationID, jobID))
	if err != nil {
		return creds, ErrSecretsUnavailable
	}
	if err := json.Unmarshal(plain, &creds); err != nil {
		return creds, ErrSecretsUnavailable
	}
	return creds, nil
}

// orderByPattern 匹配 JQL 末尾的 ORDER BY 子句（不区分大小写）；它不能放进括号里。
var orderByPattern = regexp.MustCompile(`(?is)^(.*?)\s*\border\s+by\b(.*)$`)

// ProjectJQL 生成限定项目的 JQL：为空时是 project = "KEY"，否则 project = "KEY" AND (<条件>)，
// 用户 JQL 末尾的 ORDER BY 子句保留在括号外；只有排序没有条件时是 project = "KEY" ORDER BY …。
func ProjectJQL(projectKey, jql string) string {
	base := fmt.Sprintf(`project = "%s"`, projectKey)
	jql = strings.TrimSpace(jql)
	order := ""
	if m := orderByPattern.FindStringSubmatch(jql); m != nil {
		jql, order = strings.TrimSpace(m[1]), " ORDER BY "+strings.TrimSpace(m[2])
	}
	if jql != "" {
		base += " AND (" + jql + ")"
	}
	return base + order
}

// APIDraftInput 是创建 API 导入草稿的参数（连接已由调用方校验通过）。
type APIDraftInput struct {
	API         APIInfo
	Credentials Credentials
	SpaceID     string    // 与 NewSpace 二选一
	NewSpace    *NewSpace // 已规范化
	Total       *int
	Box         *secrets.Box
}

// CreateAPIDraft 创建拉取中的 API 导入草稿（phase = fetching）：凭据加密写入，条目由后台 worker 分页拉取。
func (s Service) CreateAPIDraft(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, in APIDraftInput) (Job, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Job{}, err
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if in.SpaceID != "" {
		var ok bool
		if !membership.ValidateUUID(in.SpaceID) {
			return Job{}, ErrSpaceNotFound
		}
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id()
			  AND id = $1::uuid AND deleted_at IS NULL AND retired_at IS NULL)`, in.SpaceID).Scan(&ok); err != nil {
			return Job{}, err
		}
		if !ok {
			return Job{}, ErrSpaceNotFound
		}
	} else if in.NewSpace == nil {
		return Job{}, ErrSpaceNotFound
	}
	// 拉取中的草稿占用后台资源，每个组织最多 3 个。
	var active int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.import_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND status = 'DRAFT' AND phase = 'fetching'`).Scan(&active); err != nil {
		return Job{}, err
	}
	if active >= maxActiveFetches {
		return Job{}, ErrImportActive
	}
	jobID, err := membership.NewID()
	if err != nil {
		return Job{}, err
	}
	blob, keyID, err := sealCredentials(in.Box, organizationID, jobID, in.Credentials)
	if err != nil {
		return Job{}, err
	}
	source := SourceJiraCloud
	if in.API.Deployment == jiraapi.DeploymentServer {
		source = SourceJiraServer
	}
	label := strings.TrimPrefix(in.API.SiteURL, "https://")
	label, _ = TruncateRunes(label, 255)
	cfg := Config{Columns: []ColumnMapping{}, Statuses: []StatusMapping{}, Users: []UserMapping{},
		Options: Options{ImportOpen: true, ImportDone: true}}
	projects := true
	cfg.Options.FileProjects = &projects
	api := in.API
	doc := previewDoc{Preview: Preview{Columns: []PreviewColumn{}, RowErrors: []RowError{}},
		API: &api, Fetch: &FetchInfo{Fetched: 0, Total: in.Total}, NewSpace: in.NewSpace}
	if api.AccountIdentity != "" {
		doc.Origin = ImportSiteOrigin(api.SiteURL, api.ProjectKey)
	}
	configJSON, _ := json.Marshal(cfg)
	previewJSON, _ := json.Marshal(doc)
	operationID := newOperationID()
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.import_jobs (organization_id, id, space_id, source, source_label, status, phase, config, preview,
		                                 credentials_sealed, credentials_key_id, operation_id, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, NULLIF($2, '')::uuid, $3, $4, 'DRAFT', 'fetching', $5::jsonb, $6::jsonb,
		        $7, $8, $9, $10::uuid)`,
		jobID, in.SpaceID, source, label, configJSON, previewJSON, blob, keyID, operationID, actor.UserID); err != nil {
		return Job{}, err
	}
	// 审计元数据不含凭据与站点路径。
	if err := recordAudit(ctx, tx, organizationID, actor.UserID, actor.AuthorizationVersion, "import.draft_created", in.SpaceID, jobID, operationID,
		map[string]string{"source": source}); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, tx, organizationID, actor, jobID)
}

// ---------- worker 拉取阶段 ----------

type fetchState struct {
	Status, Phase, LeaseOwner, CreatedBy, Source string
	Preview                                      previewDoc
	Sealed                                       []byte
	KeyID                                        string
}

func (w *Worker) fetchBackoff() time.Duration {
	if w.FetchBackoff > 0 {
		return w.FetchBackoff
	}
	return defaultFetchRetry
}

// discardFetch 拉取失败或被取消：草稿改为 DISCARDED，清除凭据与已拉取的原始条目。
func discardFetch(ctx context.Context, tx pgx.Tx, jobID, code, message string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_items SET raw = '{}'::jsonb
		 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid`, jobID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET status = 'DISCARDED', phase = '', credentials_sealed = NULL, credentials_key_id = '', lease_owner = '',
		       lease_expires_at = NULL, retry_at = NULL, error_code = $2, error_message = left($3, 1000),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, jobID, code, message)
	return err
}

// lockFetch 锁行并确认任务仍是本实例持有租约的拉取中草稿；不是时 ok = false（已被取消或接手）。
func (w *Worker) lockFetch(ctx context.Context, tx pgx.Tx, jobID string) (fetchState, bool, error) {
	var st fetchState
	var preview []byte
	err := tx.QueryRow(ctx, `
		SELECT status, phase, lease_owner, created_by::text, source, preview, credentials_sealed, credentials_key_id
		  FROM openfox.import_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, jobID).
		Scan(&st.Status, &st.Phase, &st.LeaseOwner, &st.CreatedBy, &st.Source, &preview, &st.Sealed, &st.KeyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if st.Status != StatusDraft || st.Phase != "fetching" || st.LeaseOwner != w.instance() {
		return st, false, nil
	}
	if err := json.Unmarshal(preview, &st.Preview); err != nil {
		return st, false, err
	}
	if st.Preview.API == nil || st.Preview.Fetch == nil {
		return st, false, errors.New("imports: fetch state missing")
	}
	return st, true, nil
}

// fetchPass 执行一轮拉取：最多 5 页，每页在事务外请求、短事务写入。
func (w *Worker) fetchPass(ctx context.Context, organizationID, jobID string) error {
	var st fetchState
	var ok bool
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		st, ok, err = w.lockFetch(ctx, tx, jobID)
		return err
	}); err != nil || !ok {
		return err
	}
	creds, err := openCredentials(w.Secrets, organizationID, jobID, st.Sealed, st.KeyID)
	if err != nil {
		return w.discardDraft(ctx, organizationID, jobID, "SECRETS_UNAVAILABLE", "服务器无法解密保存的凭据，请重新创建导入")
	}
	api := st.Preview.API
	client, err := jiraapi.New(jiraapi.Config{Deployment: api.Deployment, SiteURL: api.SiteURL,
		Auth: jiraapi.Auth{Kind: creds.Kind, Email: creds.Email, Username: creds.Username, Secret: creds.Secret}},
		safehttp.Client(w.Safe))
	if err != nil {
		return w.discardDraft(ctx, organizationID, jobID, "JIRA_FETCH_FAILED", fetchErrorMessage(err))
	}
	jql := ProjectJQL(api.ProjectKey, api.JQL)
	fieldIDs := jiraapi.FieldIDs{Sprint: api.FieldIDs.Sprint, Points: api.FieldIDs.Points, Start: api.FieldIDs.Start, EpicLink: api.FieldIDs.EpicLink}
	cursor := jiraapi.Cursor{Token: api.Cursor.Token, StartAt: api.Cursor.StartAt}
	for page := 0; page < maxFetchPages; page++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pageCtx, cancel := context.WithTimeout(ctx, pageRequestTimeout)
		result, err := client.Search(pageCtx, jql, cursor, fieldIDs.Ids(), 0)
		var records []Record
		if err == nil {
			records, err = w.buildRecords(pageCtx, client, result.Issues, api, fieldIDs)
		}
		cancel()
		if err != nil {
			return w.fetchFailed(ctx, organizationID, jobID, err)
		}
		var proceed bool
		if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			proceed, err = w.commitPage(ctx, tx, jobID, records, result)
			return err
		}); err != nil {
			return err
		}
		if !proceed || result.Done {
			return nil
		}
		cursor = result.Next
	}
	return nil
}

// buildRecords 把 issue 转成记录，并给评论没拉全的任务补拉评论（每个任务最多 200 条）。
func (w *Worker) buildRecords(ctx context.Context, client *jiraapi.Client, issues []jiraapi.Issue, api *APIInfo, ids jiraapi.FieldIDs) ([]Record, error) {
	records := make([]Record, 0, len(issues))
	for _, issue := range issues {
		rec := BuildRecord(issue, FieldIDs{Sprint: ids.Sprint, Points: ids.Points, Start: ids.Start, EpicLink: ids.EpicLink}, api.SiteURL)
		for len(rec.Comments) < rec.CommentTotal && len(rec.Comments) < maxCommentsPerTask {
			before := len(rec.Comments)
			comments, _, err := client.Comments(ctx, issue.ID, before)
			if err != nil {
				return nil, err
			}
			if len(comments) == 0 {
				break
			}
			rec.AddComments(comments)
			if len(rec.Comments) == before {
				break
			}
		}
		records = append(records, rec)
	}
	return records, nil
}

// commitPage 在一个短事务里写入一页条目、进度与游标并续租约；拉完时计算分析并结束拉取阶段。
// proceed = false 表示任务已被取消 / 接手，应停止本轮。
func (w *Worker) commitPage(ctx context.Context, tx pgx.Tx, jobID string, records []Record, page jiraapi.Page) (bool, error) {
	st, ok, err := w.lockFetch(ctx, tx, jobID)
	if err != nil || !ok {
		return false, err
	}
	doc := st.Preview
	fetched := doc.Fetch.Fetched
	if fetched+len(records) > MaxDataRows {
		return false, discardFetch(ctx, tx, jobID, "JIRA_FETCH_FAILED", "条目超过 5000 条，请用 JQL 缩小范围")
	}
	for start := 0; start < len(records); start += insertChunk {
		end := min(start+insertChunk, len(records))
		ordinals := make([]int32, 0, end-start)
		ids := make([]string, 0, end-start)
		raws := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			ordinal := fetched + i
			ordinals = append(ordinals, int32(ordinal))
			ids = append(ids, fmt.Sprintf("row-%d", ordinal))
			raw, err := marshalRecord(records[i])
			if err != nil {
				return false, err
			}
			raws = append(raws, raw)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.import_items (organization_id, job_id, ordinal, external_id, raw)
			SELECT openfox_private.current_organization_id(), $1::uuid, v.ordinal, v.eid, v.raw::jsonb
			  FROM unnest($2::int[], $3::text[], $4::text[]) AS v(ordinal, eid, raw)`, jobID, ordinals, ids, raws); err != nil {
			return false, err
		}
	}
	fetched += len(records)
	doc.Fetch.Fetched = fetched
	doc.API.Cursor = CursorState{Token: page.Next.Token, StartAt: page.Next.StartAt}
	doc.Preview.TotalRows, doc.Preview.ImportableRows = fetched, fetched
	if !page.Done {
		previewJSON, _ := json.Marshal(doc)
		_, err := tx.Exec(ctx, `
			UPDATE openfox.import_jobs
			   SET preview = $2::jsonb, total_count = $3, lease_expires_at = clock_timestamp() + make_interval(secs => $4),
			       version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			jobID, previewJSON, fetched, leaseDuration.Seconds())
		return err == nil, err
	}
	// 拉完：fetch.total 以实际条数为准，计算分析，结束拉取阶段并清除凭据。
	total := fetched
	doc.Fetch.Total = &total
	doc.API.Cursor = CursorState{}
	var spaceID string
	var cfgRaw []byte
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(space_id::text, ''), config FROM openfox.import_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, jobID).Scan(&spaceID, &cfgRaw); err != nil {
		return false, err
	}
	var cfg Config
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		return false, err
	}
	cfg.normalize()
	analysis, infos, err := computeAnalysis(ctx, tx, jobID, spaceID, nil, cfg, true, SourceJiraCloud)
	if err != nil {
		return false, err
	}
	applyAnalysisToPreview(&doc, analysis, infos)
	previewJSON, _ := json.Marshal(doc)
	_, err = tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET preview = $2::jsonb, total_count = $3, phase = '', credentials_sealed = NULL, credentials_key_id = '',
		       lease_owner = '', lease_expires_at = NULL, retry_at = NULL, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, jobID, previewJSON, fetched)
	return false, err
}

// marshalRecord 序列化为 import_items.raw；单行超过 1 MiB 时依次丢弃评论尾部直到放得下。
func marshalRecord(rec Record) (string, error) {
	for {
		raw, err := json.Marshal(map[string]any{"record": rec})
		if err != nil {
			return "", err
		}
		if len(raw) <= maxRawRowSize || len(rec.Comments) == 0 {
			return string(raw), nil
		}
		rec.Comments = rec.Comments[:len(rec.Comments)/2]
		if len(rec.CommentIDs) > len(rec.Comments) {
			rec.CommentIDs = rec.CommentIDs[:len(rec.Comments)]
		}
	}
}

// discardDraft 在独立事务里丢弃草稿（需仍持有租约）。
func (w *Worker) discardDraft(ctx context.Context, organizationID, jobID, code, message string) error {
	return w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		if _, ok, err := w.lockFetch(ctx, tx, jobID); err != nil || !ok {
			return err
		}
		return discardFetch(ctx, tx, jobID, code, message)
	})
}

// fetchFailed 处理拉取错误：限流等 Retry-After 后继续；其他错误退避重试，连续失败 3 次丢弃草稿。
func (w *Worker) fetchFailed(ctx context.Context, organizationID, jobID string, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var apiErr *jiraapi.Error
	rateLimited := errors.As(cause, &apiErr) && apiErr.Reason == jiraapi.ReasonRateLimited
	return w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		st, ok, err := w.lockFetch(ctx, tx, jobID)
		if err != nil || !ok {
			return err
		}
		wait := time.Duration(0)
		if rateLimited {
			secs := apiErr.RetryAfter
			if secs <= 0 {
				secs = defaultRateRetry
			}
			wait = time.Duration(secs) * time.Second
		} else {
			st.Preview.API.Failures++
			if st.Preview.API.Failures >= maxFetchFailures {
				return discardFetch(ctx, tx, jobID, "JIRA_FETCH_FAILED", fetchErrorMessage(cause))
			}
			wait = w.fetchBackoff() * time.Duration(1<<(st.Preview.API.Failures-1))
		}
		previewJSON, _ := json.Marshal(st.Preview)
		_, err = tx.Exec(ctx, `
			UPDATE openfox.import_jobs
			   SET preview = $2::jsonb, attempts = attempts + $3, lease_owner = '', lease_expires_at = NULL,
			       retry_at = clock_timestamp() + make_interval(secs => $4), version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			jobID, previewJSON, boolInt(!rateLimited), wait.Seconds())
		return err
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fetchErrorMessage 是写入 error_message 的中文原因（不含凭据）。
func fetchErrorMessage(err error) string {
	switch jiraapi.ReasonOf(err) {
	case jiraapi.ReasonAuthFailed:
		return "Jira 凭证验证失败或没有权限，请检查后重新导入"
	case jiraapi.ReasonSiteNotFound:
		return "找不到 Jira 实例"
	case jiraapi.ReasonProjectNotFound:
		return "项目不存在或没有权限"
	case jiraapi.ReasonJQLInvalid:
		return "JQL 无效"
	case jiraapi.ReasonDNSFailed:
		return "Jira 域名无法解析"
	case jiraapi.ReasonTLSFailed:
		return "Jira 的 HTTPS 证书未通过校验"
	case jiraapi.ReasonBlockedAddress:
		return "Jira 地址指向内网或保留地址，已被拦截"
	case jiraapi.ReasonInvalidURL:
		return "Jira 地址格式不合法"
	case jiraapi.ReasonRateLimited:
		return "Jira 持续限流，拉取已停止"
	}
	return "无法从 Jira 拉取条目，请稍后重试"
}
