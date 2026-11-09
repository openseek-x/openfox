package automations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/secrets"
)

// Webhook：令牌只存 SHA-256 哈希，签名密钥用 secrets.Box 密封；接收端在组织事务里按触发器串行化。

const (
	webhookTokenBytes   = 32
	webhookSecretBytes  = 32
	webhookSecretPrefix = "whsec_"
	// WebhookBodyLimit 是请求体上限（256 KB），超出返回 413。
	WebhookBodyLimit    = 256 << 10
	webhookDedupeWindow = 10 * time.Minute
	maxStoredHeaders    = 50
	maskedHeaderValue   = "••••"
	bodyContextLimit    = 8 << 10
)

// maskedHeaders 是存储时需要遮挡值的签名相关请求头（键为规范化后的名称）。
var maskedHeaders = map[string]bool{
	"X-Openfox-Signature": true,
	"X-Hub-Signature":     true,
	"X-Hub-Signature-256": true,
}

// Inbound 是接收端收到的一次 POST。令牌在 API 层从路径取出，这里只使用哈希。
type Inbound struct {
	TokenHash []byte
	Header    http.Header
	Body      []byte
	SourceIP  string
}

// Outcome 是接收端的处理结果，API 层据此写 HTTP 响应。
type Outcome struct {
	// Found 为 false 表示令牌对应的触发器已不存在（或已轮换），返回 404。
	Found      bool
	HTTPStatus int
	Delivery   Delivery
	// Pending 只在投递被派发时存在：运行要在创建者身份的事务里启动（见 ApplyWebhookRun）。
	Pending *PendingRun
}

// WebhookLookup 是 resolve_webhook_trigger 找到的组织、自动化与触发器。
type WebhookLookup struct {
	OrganizationID string
	AutomationID   string
	TriggerID      string
}

// newWebhookToken 生成 32 字节随机令牌（base64url）及其 SHA-256 哈希。
func newWebhookToken() (string, []byte, error) {
	raw := make([]byte, webhookTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, TokenHash(token), nil
}

// TokenHash 返回令牌的 SHA-256。数据库与限流都只使用哈希，不保存明文。
func TokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// webhookURL 拼接对外 URL，publicURL 末尾的 / 会被去掉。
func webhookURL(publicURL, token string) string {
	return strings.TrimRight(publicURL, "/") + "/api/v1/hooks/" + token
}

// newWebhookSecret 生成 whsec_ 前缀加 32 字节随机数（base64url）的签名密钥。
func newWebhookSecret() (string, error) {
	raw := make([]byte, webhookSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return webhookSecretPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// webhookSecretAAD 把密文绑定到组织与触发器，防止密文被挪用到其他触发器。
func webhookSecretAAD(organizationID, triggerID string) string {
	return "webhook-secret-v1\x00" + organizationID + "\x00" + triggerID
}

// RegenerateWebhookURL 换一个新令牌，旧 URL 立即失效；新 URL 只在本次响应中返回。
func (s Service) RegenerateWebhookURL(ctx context.Context, tx pgx.Tx, spaceID, automationID, triggerID string) (string, error) {
	if err := requireWebhookTrigger(ctx, tx, spaceID, automationID, triggerID); err != nil {
		return "", err
	}
	token, hash, err := newWebhookToken()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.automation_triggers SET webhook_token_hash = $3, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND automation_id = $1::uuid AND id = $2::uuid`,
		automationID, triggerID, hash); err != nil {
		return "", err
	}
	return webhookURL(s.PublicURL, token), nil
}

// GetSigningSecret 返回签名密钥明文。Box 未配置时 503；还没有密钥时 404。
func (s Service) GetSigningSecret(ctx context.Context, tx pgx.Tx, organizationID, spaceID, automationID, triggerID string) (string, error) {
	if s.Box == nil {
		return "", ErrSecretsDisabled
	}
	if err := requireWebhookTrigger(ctx, tx, spaceID, automationID, triggerID); err != nil {
		return "", err
	}
	sealed, ok, err := loadSealedSecret(ctx, tx, triggerID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrSecretMissing
	}
	plain, err := s.Box.Open(sealed, webhookSecretAAD(organizationID, triggerID))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// ResetSigningSecret 生成或重置签名密钥，旧密钥立即失效。
func (s Service) ResetSigningSecret(ctx context.Context, tx pgx.Tx, organizationID, spaceID, automationID, triggerID string) (string, error) {
	if s.Box == nil {
		return "", ErrSecretsDisabled
	}
	if err := requireWebhookTrigger(ctx, tx, spaceID, automationID, triggerID); err != nil {
		return "", err
	}
	secret, err := newWebhookSecret()
	if err != nil {
		return "", err
	}
	sealed, err := s.Box.Seal([]byte(secret), webhookSecretAAD(organizationID, triggerID))
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.automation_triggers
		   SET webhook_secret_ciphertext = $3, webhook_secret_nonce = $4, webhook_secret_key_id = $5,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND automation_id = $1::uuid AND id = $2::uuid`,
		automationID, triggerID, sealed.Ciphertext, sealed.Nonce, sealed.KeyID); err != nil {
		return "", err
	}
	return secret, nil
}

// loadSealedSecret 读取触发器的密文；没有密钥时 ok 为 false。
func loadSealedSecret(ctx context.Context, tx pgx.Tx, triggerID string) (secrets.Sealed, bool, error) {
	var ciphertext, nonce []byte
	var keyID *string
	err := tx.QueryRow(ctx, `
		SELECT webhook_secret_ciphertext, webhook_secret_nonce, webhook_secret_key_id
		  FROM openfox.automation_triggers
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, triggerID).Scan(&ciphertext, &nonce, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return secrets.Sealed{}, false, ErrTriggerNotFound
	}
	if err != nil {
		return secrets.Sealed{}, false, err
	}
	if ciphertext == nil {
		return secrets.Sealed{}, false, nil
	}
	sealed := secrets.Sealed{Ciphertext: ciphertext, Nonce: nonce}
	if keyID != nil {
		sealed.KeyID = *keyID
	}
	return sealed, true, nil
}

// requireWebhookTrigger 确认触发器属于该空间的这个自动化，且类型是 webhook。
func requireWebhookTrigger(ctx context.Context, tx pgx.Tx, spaceID, automationID, triggerID string) error {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM openfox.automation_triggers t
		      JOIN openfox.automations a ON a.organization_id = t.organization_id AND a.id = t.automation_id
		     WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $3::uuid
		       AND t.automation_id = $2::uuid AND a.space_id = $1::uuid AND t.kind = 'webhook')`,
		spaceID, automationID, triggerID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrTriggerNotFound
	}
	return nil
}

// Receive 处理一次 Webhook 投递。调用方已在组织事务里，并通过 resolve_webhook_trigger 找到了触发器。
// 拒绝（IP、签名）、去重与停用都作为正常结果返回并写入投递记录；只有数据库错误才返回 error。
func (s Service) Receive(ctx context.Context, tx pgx.Tx, lookup WebhookLookup, in Inbound, now time.Time) (Outcome, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, lookup.TriggerID); err != nil {
		return Outcome{}, err
	}
	state, ok, err := loadWebhookState(ctx, tx, lookup.TriggerID, in.TokenHash)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{Found: false}, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.automation_triggers SET last_source_ip = $2
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, lookup.TriggerID, in.SourceIP); err != nil {
		return Outcome{}, err
	}
	deliveryID, err := newUUID()
	if err != nil {
		return Outcome{}, err
	}
	provider, event := identifyProvider(in.Header)
	dedupeKey, dedupeSource := dedupeKeyFor(in.Header, in.Body)
	entry := deliveryEntry{
		ID: deliveryID, AutomationID: lookup.AutomationID, TriggerID: lookup.TriggerID,
		Provider: provider, Event: event, DedupeKey: dedupeKey, DedupeSource: dedupeSource,
		ContentType: truncateRunes(in.Header.Get("Content-Type"), 128), SourceIP: truncateRunes(in.SourceIP, 64),
		Headers: storedHeaders(in.Header), Body: in.Body,
	}

	if !IPAllowed(state.Allowlist, in.SourceIP) {
		entry.Status, entry.Signature, entry.ResponseStatus = "rejected", SignatureIPBlocked, http.StatusForbidden
		return s.recordDelivery(ctx, tx, entry)
	}
	signature := VerifySignature(state.SignatureRequired, s.openSecret(state.Sealed, lookup), in.Header, in.Body, now)
	if signature != SignatureValid && signature != SignatureNotRequired {
		entry.Status, entry.Signature, entry.ResponseStatus = "rejected", signature, http.StatusUnauthorized
		return s.recordDelivery(ctx, tx, entry)
	}
	entry.Signature = signature
	duplicate, err := hasRecentDelivery(ctx, tx, lookup.TriggerID, dedupeKey, now)
	if err != nil {
		return Outcome{}, err
	}
	if duplicate || !state.AutomationEnabled || !state.TriggerEnabled {
		entry.Status, entry.ResponseStatus = "ignored", http.StatusOK
		return s.recordDelivery(ctx, tx, entry)
	}
	entry.Status, entry.ResponseStatus = "dispatched", http.StatusAccepted
	outcome, err := s.recordDelivery(ctx, tx, entry)
	if err != nil {
		return Outcome{}, err
	}
	outcome.Pending = &PendingRun{
		Kind: "webhook", AutomationID: lookup.AutomationID, CreatorID: state.CreatorID, CreatorVersion: state.CreatorVersion,
		Source: Source{
			TriggerID: lookup.TriggerID, Kind: "webhook", Title: webhookTitle(provider, event),
			Payload:   map[string]any{"provider": provider, "event": event, "delivery_id": deliveryID},
			Context:   webhookContext(provider, event, in.Body),
			DedupeKey: deliveryID,
		},
	}
	return outcome, nil
}

// ApplyWebhookRun 在创建者身份的事务里为投递启动运行，并把运行 ID 记到投递上。
// 投递派发后自动化被停用时，投递记为 ignored。
func (s Service) ApplyWebhookRun(ctx context.Context, tx pgx.Tx, organizationID string, p PendingRun) error {
	run, err := s.StartRun(ctx, tx, organizationID, p.AutomationID, p.Source)
	if errors.Is(err, ErrDisabled) {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.webhook_deliveries SET status = 'ignored'
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, p.Source.Payload["delivery_id"])
		return err
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.webhook_deliveries SET automation_run_id = $2::uuid
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, p.Source.Payload["delivery_id"], run.ID)
	return err
}

// webhookState 是接收端判断需要的触发器与自动化状态。
type webhookState struct {
	TriggerEnabled    bool
	AutomationEnabled bool
	SignatureRequired bool
	Allowlist         []string
	Sealed            *secrets.Sealed
	CreatorID         string
	CreatorVersion    uint64
}

// loadWebhookState 读取接收端状态；令牌哈希不匹配（已轮换）视为不存在。
func loadWebhookState(ctx context.Context, tx pgx.Tx, triggerID string, tokenHash []byte) (webhookState, bool, error) {
	var st webhookState
	var ciphertext, nonce []byte
	var keyID *string
	err := tx.QueryRow(ctx, `
		SELECT t.enabled, a.enabled, t.signature_required, t.ip_allowlist,
		       t.webhook_secret_ciphertext, t.webhook_secret_nonce, t.webhook_secret_key_id,
		       a.created_by::text, COALESCE(m.authorization_version, 1)
		  FROM openfox.automation_triggers t
		  JOIN openfox.automations a ON a.organization_id = t.organization_id AND a.id = t.automation_id
		  LEFT JOIN openfox.organization_members m ON m.organization_id = a.organization_id AND m.user_id = a.created_by
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid
		   AND t.kind = 'webhook' AND t.webhook_token_hash = $2`, triggerID, tokenHash).Scan(
		&st.TriggerEnabled, &st.AutomationEnabled, &st.SignatureRequired, &st.Allowlist,
		&ciphertext, &nonce, &keyID, &st.CreatorID, &st.CreatorVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return webhookState{}, false, nil
	}
	if err != nil {
		return webhookState{}, false, err
	}
	if ciphertext != nil {
		sealed := secrets.Sealed{Ciphertext: ciphertext, Nonce: nonce}
		if keyID != nil {
			sealed.KeyID = *keyID
		}
		st.Sealed = &sealed
	}
	return st, true, nil
}

// openSecret 解封签名密钥。没有密钥、Box 未配置或解封失败时返回 nil：要求签名时校验会因缺少密钥而拒绝（fail closed）。
func (s Service) openSecret(sealed *secrets.Sealed, lookup WebhookLookup) []byte {
	if sealed == nil || s.Box == nil {
		return nil
	}
	plain, err := s.Box.Open(*sealed, webhookSecretAAD(lookup.OrganizationID, lookup.TriggerID))
	if err != nil {
		return nil
	}
	return plain
}

// hasRecentDelivery 判断 10 分钟内是否已有同一去重键的非拒绝投递（不含重放）。
// 拒绝的投递不参与去重，否则攻击者可以用错误签名占用去重键，让合法投递被误判为重复。
func hasRecentDelivery(ctx context.Context, tx pgx.Tx, triggerID, dedupeKey string, now time.Time) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM openfox.webhook_deliveries
		     WHERE organization_id = openfox_private.current_organization_id() AND trigger_id = $1::uuid
		       AND dedupe_key = $2 AND replay_of IS NULL AND status <> 'rejected'
		       AND received_at > $3::timestamptz - make_interval(secs => $4))`,
		triggerID, dedupeKey, now, webhookDedupeWindow.Seconds()).Scan(&exists)
	return exists, err
}

// deliveryEntry 是一次投递的待写入内容。
type deliveryEntry struct {
	ID             string
	AutomationID   string
	TriggerID      string
	Status         string
	Signature      string
	Provider       string
	Event          string
	DedupeKey      string
	DedupeSource   string
	ContentType    string
	SourceIP       string
	Headers        map[string]string
	Body           []byte
	ResponseStatus int
	ReplayOf       string
}

// recordDelivery 写入投递记录并返回对外形态。
func (s Service) recordDelivery(ctx context.Context, tx pgx.Tx, e deliveryEntry) (Outcome, error) {
	headers, err := json.Marshal(nonNilHeaders(e.Headers))
	if err != nil {
		return Outcome{}, err
	}
	var receivedAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.webhook_deliveries (
			organization_id, id, automation_id, trigger_id, status, signature_status, provider, event,
			dedupe_key, dedupe_source, content_type, source_ip, headers, body, response_status, replay_of
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7,
		          $8, $9, $10, $11, $12::jsonb, $13, $14, NULLIF($15, '')::uuid)
		RETURNING received_at`,
		e.ID, e.AutomationID, e.TriggerID, e.Status, e.Signature, e.Provider, e.Event,
		e.DedupeKey, e.DedupeSource, e.ContentType, e.SourceIP, string(headers), e.Body, e.ResponseStatus, e.ReplayOf).Scan(&receivedAt)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{
		Found: true, HTTPStatus: e.ResponseStatus,
		Delivery: Delivery{
			ID: e.ID, TriggerID: e.TriggerID, Status: e.Status, SignatureStatus: e.Signature,
			Provider: e.Provider, Event: e.Event, ReceivedAt: receivedAt, Attempts: 1,
		},
	}, nil
}

func nonNilHeaders(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return values
}

// identifyProvider 按请求头识别来源：X-GitHub-Event → github，X-Gitlab-Event → gitlab，否则 generic。
func identifyProvider(header http.Header) (string, string) {
	if event := header.Get("X-GitHub-Event"); event != "" {
		return "github", truncateRunes(event, 128)
	}
	if event := header.Get("X-Gitlab-Event"); event != "" {
		return "gitlab", truncateRunes(event, 128)
	}
	return "generic", truncateRunes(header.Get("X-OpenFox-Event"), 128)
}

// dedupeKeyFor 优先取投递 ID 请求头，否则使用 body 的 SHA-256。
func dedupeKeyFor(header http.Header, body []byte) (string, string) {
	for _, name := range []string{"X-OpenFox-Delivery", "X-GitHub-Delivery", "X-Request-Id"} {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return truncateRunes(value, 256), "header"
		}
	}
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum[:]), "body_hash"
}

// storedHeaders 保留至多 50 个请求头（按名称排序，结果稳定），签名相关请求头的值替换为 ••••。
func storedHeaders(header http.Header) map[string]string {
	names := make([]string, 0, len(header))
	for name := range header {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(map[string]string, maxStoredHeaders)
	for _, name := range names {
		if len(out) >= maxStoredHeaders {
			break
		}
		canonical := http.CanonicalHeaderKey(name)
		value := strings.Join(header.Values(name), ", ")
		if maskedHeaders[canonical] {
			value = maskedHeaderValue
		}
		out[canonical] = truncateRunes(value, 1000)
	}
	return out
}

func webhookTitle(provider, event string) string {
	if event == "" {
		return "收到 " + provider + " Webhook"
	}
	return "收到 " + provider + " Webhook · " + event
}

// webhookContext 生成触发上下文；body 截断到 8 KB，非 UTF-8 内容只给提示，不把二进制放进消息。
func webhookContext(provider, event string, body []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- 触发原因：Webhook（provider %s，event %s）\n", provider, event)
	if !utf8.Valid(body) {
		fmt.Fprintf(&b, "- 请求体：非文本内容，共 %d 字节\n", len(body))
		return b.String()
	}
	preview := body
	if len(preview) > bodyContextLimit {
		preview = preview[:bodyContextLimit]
		for !utf8.Valid(preview) {
			preview = preview[:len(preview)-1]
		}
	}
	b.WriteString("- 请求体（截断到 8 KB）：\n")
	b.Write(preview)
	return b.String()
}
