package main

// 协作 CLI 的公共部分：依赖注入、flag 解析、会话（profile/服务器/令牌/组织）解析、错误与退出码、
// 分页、表格输出。命令实现分别在 cli_auth.go、cli_task.go、cli_conv.go。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"openfox/internal/cliclient"
	"openfox/internal/cliconfig"
	"openfox/internal/daemon"
	"openfox/internal/identity"
)

// 协作 CLI 的退出码。computer register 的 2–5 语义保持不变（见 main.go），这里对 CLI 命令另行定义。
const (
	cliExitGeneral     = 1
	cliExitUsage       = 2 // 参数错误（含服务端 400）
	cliExitAuth        = 4 // 认证失败（401/403）
	cliExitUnreachable = 5 // 中心不可达或暂时不可用（网络错误、502/503/504）
	cliExitNotFound    = 6
	cliExitConflict    = 7 // 409

	// maxAllItems 是 --all 最多拉取的条数。
	maxAllItems = 1000
	// maxStdinBytes 限制从 stdin 读取的正文/令牌大小。
	maxStdinBytes = 70 * 1024
)

var (
	uuidPattern        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	displayIDPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+$`)
	idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
)

// cliDeps 是可替换依赖（测试注入标准输入输出、环境变量、配置目录与 HTTP 客户端）。
type cliDeps struct {
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	getenv     func(string) string
	configDir  func() (string, error)
	httpClient *http.Client
	newKey     func() string
	sleep      func(time.Duration)
}

func defaultCLIDeps() cliDeps {
	return cliDeps{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, configDir: daemon.DefaultConfigDir, newKey: randomKey}
}

// randomKey 生成 32 位十六进制字符串，满足 Idempotency-Key 的格式（8–128 位字母数字）。
func randomKey() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}

// cli 是一次命令调用的运行环境。
type cli struct {
	cliDeps
	jsonErrors bool // --output json 时错误以 JSON 输出到 stderr
	warned     bool
}

// cliError 是带结构化信息的错误；Error() 按输出模式渲染为文本或 JSON。
type cliError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	asJSON    bool
}

func (e *cliError) Error() string {
	if e.asJSON {
		body := map[string]any{"code": e.Code, "message": e.Message}
		if e.RequestID != "" {
			body["request_id"] = e.RequestID
		}
		if e.Status != 0 {
			body["status"] = e.Status
		}
		encoded, _ := json.Marshal(map[string]any{"error": body})
		return string(encoded)
	}
	text := "error: " + e.Message
	var extra []string
	if e.Code != "" && e.Code != "USAGE" {
		extra = append(extra, "code="+e.Code)
	}
	if e.RequestID != "" {
		extra = append(extra, "request_id="+e.RequestID)
	}
	if len(extra) > 0 {
		text += " (" + strings.Join(extra, ", ") + ")"
	}
	return text
}

func usageError(format string, args ...any) error {
	return withExitCode(cliExitUsage, &cliError{Code: "USAGE", Message: fmt.Sprintf(format, args...)})
}

func failure(code int, errCode, format string, args ...any) error {
	return withExitCode(code, &cliError{Code: errCode, Message: fmt.Sprintf(format, args...)})
}

// wrapClientError 把客户端错误映射为退出码：401/403→4，404→6，409→7，400/422→2，502/503/504 与网络错误→5。
func wrapClientError(err error) error {
	var api *cliclient.APIError
	var network *cliclient.NetworkError
	switch {
	case errors.As(err, &api):
		code := cliExitGeneral
		switch {
		case api.Status == http.StatusUnauthorized || api.Status == http.StatusForbidden:
			code = cliExitAuth
		case api.Status == http.StatusNotFound:
			code = cliExitNotFound
		case api.Status == http.StatusConflict:
			code = cliExitConflict
		case api.Status == http.StatusBadRequest || api.Status == http.StatusUnprocessableEntity:
			code = cliExitUsage
		case api.Status == http.StatusBadGateway || api.Status == http.StatusServiceUnavailable || api.Status == http.StatusGatewayTimeout:
			code = cliExitUnreachable
		}
		return withExitCode(code, &cliError{Status: api.Status, Code: api.Code, Message: api.Message, RequestID: api.RequestID})
	case errors.As(err, &network):
		return withExitCode(cliExitUnreachable, &cliError{Code: "UNREACHABLE", Message: network.Error()})
	default:
		return withExitCode(cliExitGeneral, &cliError{Code: "ERROR", Message: err.Error()})
	}
}

// runCLI 分派 auth/profile/org/space/task/agent/conversation/run/search/me。
func runCLI(group string, args []string, deps cliDeps) error {
	c := &cli{cliDeps: deps}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := c.dispatch(ctx, group, args)
	var coded *exitError
	if errors.As(err, &coded) {
		var ce *cliError
		if errors.As(coded.err, &ce) {
			ce.asJSON = c.jsonErrors
		}
	}
	return err
}

func (c *cli) dispatch(ctx context.Context, group string, args []string) error {
	table := map[string]func(context.Context, []string) error{
		"auth": c.runAuth, "profile": c.runProfile, "org": c.runOrg, "me": c.runMe,
		"space": c.runSpace, "task": c.runTask, "agent": c.runAgent, "search": c.runSearch,
		"conversation": c.runConversation, "run": c.runRunGroup,
	}
	handler, ok := table[group]
	if !ok {
		return usageError("unknown command %q", group)
	}
	return handler(ctx, args)
}

// ---- flag 解析 ----

// commonFlags 是所有访问中心的命令共有的选项。
type commonFlags struct{ profile, org, output string }

func (cf *commonFlags) register(fs *flag.FlagSet, withOrg bool) {
	fs.StringVar(&cf.profile, "profile", "", "profile name (default: OPENFOX_PROFILE or the current profile)")
	if withOrg {
		fs.StringVar(&cf.org, "org", "", "organization id or slug (default: OPENFOX_ORG or the profile organization)")
	}
	fs.StringVar(&cf.output, "output", "table", "output format: table or json")
}

// pageFlags 是列表命令的分页选项。
type pageFlags struct {
	limit  int
	cursor string
	all    bool
}

func (pf *pageFlags) register(fs *flag.FlagSet) {
	fs.IntVar(&pf.limit, "limit", 0, "page size, 1-100 (default: server default)")
	fs.StringVar(&pf.cursor, "cursor", "", "continue from this cursor (the next_cursor of the previous page)")
	fs.BoolVar(&pf.all, "all", false, fmt.Sprintf("fetch every page, at most %d items", maxAllItems))
}

// stringList 是可重复的字符串 flag。
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parse 解析 flag，允许选项与位置参数交错；"--" 之后全部视为位置参数。返回位置参数。
func (c *cli) parse(fs *flag.FlagSet, cf *commonFlags, usage string, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var tail []string
	for i, arg := range args {
		if arg == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fs.SetOutput(c.stdout)
				fmt.Fprintln(c.stdout, "usage: "+usage)
				fs.PrintDefaults()
				return nil, errHelpShown
			}
			return nil, usageError("%v\nusage: %s", err, usage)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	positional = append(positional, tail...)
	if cf != nil {
		switch cf.output {
		case "json":
			c.jsonErrors = true
		case "table":
		default:
			return nil, usageError("--output must be table or json")
		}
	}
	return positional, nil
}

// parsePaged 在 parse 之外校验分页选项，保证非法 --limit 先于任何网络请求被拒绝。
func (c *cli) parsePaged(fs *flag.FlagSet, cf *commonFlags, pf *pageFlags, usage string, args []string) ([]string, error) {
	positional, err := c.parse(fs, cf, usage, args)
	if err != nil {
		return nil, err
	}
	return positional, pf.validate()
}

// errHelpShown 表示已打印帮助，调用方应以成功退出。
var errHelpShown = errors.New("help shown")

func ignoreHelp(err error) error {
	if errors.Is(err, errHelpShown) {
		return nil
	}
	return err
}

func (pf *pageFlags) validate() error {
	if pf.limit < 0 || pf.limit > 100 {
		return usageError("--limit must be between 1 and 100")
	}
	return nil
}

// ---- 会话 ----

type cliSession struct {
	*cli
	cf      *commonFlags
	client  *cliclient.Client
	profile string
	org     string // 组织 UUID（needOrg 时已解析）
}

func (c *cli) store() (cliconfig.Store, error) {
	dir, err := c.configDir()
	if err != nil {
		return cliconfig.Store{}, err
	}
	return cliconfig.Store{Dir: filepath.Join(dir, "cli")}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// open 解析 profile、服务器、令牌与组织。优先级：命令行 > 环境变量 > 本地配置。
func (c *cli) open(ctx context.Context, cf *commonFlags, needOrg bool) (*cliSession, error) {
	store, err := c.store()
	if err != nil {
		return nil, wrapClientError(err)
	}
	file, err := store.Load()
	if err != nil {
		return nil, wrapClientError(err)
	}
	name := firstNonEmpty(cf.profile, c.getenv("OPENFOX_PROFILE"), file.Current)
	profile, known := file.Profiles[name]
	server := firstNonEmpty(c.getenv("OPENFOX_SERVER"), profile.Server)
	if name != "" && !known && server == "" {
		return nil, failure(cliExitNotFound, "PROFILE_NOT_FOUND", "profile %q not found; run `openfox auth login --profile %s --server <url>`", name, name)
	}
	if server == "" {
		return nil, usageError("no server configured; run `openfox auth login --server <url>` or set OPENFOX_SERVER")
	}
	token := strings.TrimSpace(c.getenv("OPENFOX_TOKEN"))
	if token == "" {
		if name == "" {
			return nil, failure(cliExitAuth, "NOT_LOGGED_IN", "not logged in; run `openfox auth login` or set OPENFOX_TOKEN")
		}
		if token, err = store.ReadToken(name); errors.Is(err, cliconfig.ErrNoToken) {
			return nil, failure(cliExitAuth, "NOT_LOGGED_IN", "profile %q has no token; run `openfox auth login --profile %s`", name, name)
		} else if err != nil {
			return nil, wrapClientError(err)
		}
	}
	client, err := c.newClient(server, token)
	if err != nil {
		return nil, usageError("%v", err)
	}
	session := &cliSession{cli: c, cf: cf, client: client, profile: name}
	session.org = firstNonEmpty(cf.org, c.getenv("OPENFOX_ORG"), profile.Org)
	if needOrg {
		if session.org == "" {
			return nil, usageError("no organization selected; use --org, OPENFOX_ORG or `openfox org use <slug>`")
		}
		if !uuidPattern.MatchString(session.org) {
			if session.org, err = session.resolveOrg(ctx, session.org); err != nil {
				return nil, err
			}
		}
	}
	return session, nil
}

// newClient 校验令牌格式并创建客户端；明文 http 的远程地址给出警告。
func (c *cli) newClient(server, token string) (*cliclient.Client, error) {
	if !identity.IsPersonalAccessToken(token) {
		return nil, errors.New("the token is not a personal access token (expected the ofx_pat_ prefix)")
	}
	client, err := cliclient.New(server, token, version)
	if err != nil {
		return nil, err
	}
	if c.httpClient != nil {
		client.HTTP = c.httpClient
	}
	client.Sleep = c.sleep
	if cliclient.InsecureRemote(client.BaseURL) && !c.warned {
		c.warned = true
		fmt.Fprintln(c.stderr, "warning: the server URL uses plain http; the access token is sent unencrypted. Use https.")
	}
	return client, nil
}

func (s *cliSession) orgPath(rest string) string { return "/v1/organizations/" + s.org + "/" + rest }

// call 发请求并返回响应体；错误已映射为退出码。
func (s *cliSession) call(ctx context.Context, req cliclient.Request) ([]byte, error) {
	response, err := s.client.Do(ctx, req)
	if err != nil {
		return nil, wrapClientError(err)
	}
	return response.Body, nil
}

func (s *cliSession) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	return s.call(ctx, cliclient.Request{Method: http.MethodGet, Path: path, Query: query})
}

// write 发写请求。key 非空表示请求自带幂等保证（Idempotency-Key 头）；clientID 表示 body 里带 client_*_id。
func (s *cliSession) write(ctx context.Context, method, path string, body any, key string, clientID bool) ([]byte, error) {
	return s.call(ctx, cliclient.Request{Method: method, Path: path, Body: body, IdempotencyKey: key, Idempotent: key != "" || clientID})
}

// idempotencyKey 返回 --idempotency-key 或新生成的键。
func (c *cli) idempotencyKey(given string) (string, error) {
	if given == "" {
		return c.newKey(), nil
	}
	if !idempotencyPattern.MatchString(given) {
		return "", usageError("--idempotency-key must be 8-128 characters of letters, digits, '_' or '-'")
	}
	return given, nil
}

// ---- JSON / 表格输出 ----

func decodeObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, failure(cliExitGeneral, "BAD_RESPONSE", "unexpected response from center: %v", err)
	}
	return value, nil
}

// field 按点号路径取字符串化的字段；缺失返回空串。
func field(value map[string]any, path string) string {
	var current any = value
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[part]
	}
	switch v := current.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	default:
		encoded, _ := json.Marshal(v)
		return string(encoded)
	}
}

func objectsAt(value map[string]any, key string) []map[string]any {
	list, _ := value[key].([]any)
	items := make([]map[string]any, 0, len(list))
	for _, entry := range list {
		if object, ok := entry.(map[string]any); ok {
			items = append(items, object)
		}
	}
	return items
}

// cell 清理控制字符（防止服务端内容改写终端）并截断过长文本。
func cell(text string, max int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	if runes := []rune(text); max > 0 && len(runes) > max {
		return string(runes[:max-1]) + "…"
	}
	return text
}

func (c *cli) printRaw(body []byte) {
	fmt.Fprintln(c.stdout, strings.TrimRight(string(body), "\n"))
}

func (c *cli) printTable(headers []string, rows [][]string) {
	writer := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, strings.Join(headers, "\t"))
	for _, row := range rows {
		cleaned := make([]string, len(row))
		for i, value := range row {
			cleaned[i] = cell(value, 80)
		}
		fmt.Fprintln(writer, strings.Join(cleaned, "\t"))
	}
	_ = writer.Flush()
}

// printPairs 以「名称: 值」逐行输出单个对象。
func (c *cli) printPairs(pairs [][2]string) {
	for _, pair := range pairs {
		fmt.Fprintf(c.stdout, "%-14s %s\n", pair[0]+":", cell(pair[1], 500))
	}
}

// render 在 json 模式原样输出响应体，table 模式交给 table 回调。
func (s *cliSession) render(body []byte, table func(map[string]any)) error {
	if s.cf.output == "json" {
		s.printRaw(body)
		return nil
	}
	value, err := decodeObject(body)
	if err != nil {
		return err
	}
	table(value)
	return nil
}

// ---- 分页 ----

// cursorValue 把 next_cursor 的 JSON 取值转为查询参数；空串/null/0 表示没有下一页。
func cursorValue(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	switch text {
	case "", "null", `""`, "0":
		return ""
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return asString
	}
	return text
}

// listPage 读取一页或（--all）多页。listKey 是响应里数组字段名，cursorParam 是游标查询参数名。
// --all 时每次请求的 limit 不超过剩余额度，因此总在页边界停止，next_cursor 可直接用于继续。
func (s *cliSession) listPage(ctx context.Context, path string, query url.Values, listKey, cursorParam string, pf *pageFlags) ([]byte, error) {
	if err := pf.validate(); err != nil {
		return nil, err
	}
	if query == nil {
		query = url.Values{}
	}
	if !pf.all {
		if pf.limit > 0 {
			query.Set("limit", strconv.Itoa(pf.limit))
		}
		if pf.cursor != "" {
			query.Set(cursorParam, pf.cursor)
		}
		return s.get(ctx, path, query)
	}
	var items []json.RawMessage
	var base map[string]json.RawMessage
	cursor := pf.cursor
	for pages := 0; pages <= maxAllItems; pages++ {
		pageSize := min(100, maxAllItems-len(items))
		if pf.limit > 0 {
			pageSize = min(pageSize, pf.limit)
		}
		page := url.Values{}
		for key, values := range query {
			page[key] = values
		}
		page.Set("limit", strconv.Itoa(pageSize))
		if cursor != "" {
			page.Set(cursorParam, cursor)
		}
		body, err := s.get(ctx, path, page)
		if err != nil {
			return nil, err
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, failure(cliExitGeneral, "BAD_RESPONSE", "unexpected response from center: %v", err)
		}
		var pageItems []json.RawMessage
		if err := json.Unmarshal(decoded[listKey], &pageItems); err != nil {
			return nil, failure(cliExitGeneral, "BAD_RESPONSE", "unexpected response from center: missing %q", listKey)
		}
		items = append(items, pageItems...)
		base = decoded
		next := cursorValue(decoded["next_cursor"])
		if next == "" || len(items) >= maxAllItems {
			if next != "" {
				fmt.Fprintf(s.stderr, "stopped after %d items; continue with --cursor %s\n", len(items), next)
			}
			break
		}
		if next == cursor || len(pageItems) == 0 {
			return nil, failure(cliExitGeneral, "BAD_RESPONSE", "pagination did not advance")
		}
		cursor = next
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	base[listKey] = encoded
	return json.Marshal(base)
}

// ---- 引用解析（UUID 或名称） ----

func (s *cliSession) resolveOrg(ctx context.Context, ref string) (string, error) {
	body, err := s.get(ctx, "/v1/me/organizations", nil)
	if err != nil {
		return "", err
	}
	value, err := decodeObject(body)
	if err != nil {
		return "", err
	}
	for _, org := range objectsAt(value, "organizations") {
		if strings.EqualFold(field(org, "slug"), ref) || strings.EqualFold(field(org, "id"), ref) {
			return field(org, "id"), nil
		}
	}
	return "", failure(cliExitNotFound, "ORGANIZATION_NOT_FOUND", "organization %q not found", ref)
}

// resolveByName 在列表里按名称（不区分大小写）找唯一项；重名时报错。
func resolveByName(items []map[string]any, nameKey, ref, what string) (string, error) {
	var matches []string
	for _, item := range items {
		if strings.EqualFold(field(item, nameKey), ref) {
			matches = append(matches, field(item, "id"))
		}
	}
	switch len(matches) {
	case 0:
		return "", failure(cliExitNotFound, strings.ToUpper(what)+"_NOT_FOUND", "%s %q not found", what, ref)
	case 1:
		return matches[0], nil
	default:
		return "", usageError("%s name %q is ambiguous; use its id", what, ref)
	}
}

func (s *cliSession) resolveSpace(ctx context.Context, ref string) (string, error) {
	if uuidPattern.MatchString(ref) {
		return ref, nil
	}
	body, err := s.get(ctx, s.orgPath("spaces"), nil)
	if err != nil {
		return "", err
	}
	value, err := decodeObject(body)
	if err != nil {
		return "", err
	}
	return resolveByName(objectsAt(value, "spaces"), "prefix", ref, "space")
}

func (s *cliSession) resolveStatus(ctx context.Context, ref string) (string, error) {
	if uuidPattern.MatchString(ref) {
		return ref, nil
	}
	body, err := s.get(ctx, s.orgPath("task-statuses"), nil)
	if err != nil {
		return "", err
	}
	value, err := decodeObject(body)
	if err != nil {
		return "", err
	}
	return resolveByName(objectsAt(value, "statuses"), "name", ref, "status")
}

func (s *cliSession) resolveLabel(ctx context.Context, spaceID, ref string) (string, error) {
	if uuidPattern.MatchString(ref) {
		return ref, nil
	}
	body, err := s.get(ctx, s.orgPath("labels"), url.Values{"scope": {"picker"}, "space_id": {spaceID}})
	if err != nil {
		return "", err
	}
	value, err := decodeObject(body)
	if err != nil {
		return "", err
	}
	return resolveByName(objectsAt(value, "labels"), "name", ref, "label")
}

func (s *cliSession) resolveAgent(ctx context.Context, spaceID, ref string) (string, error) {
	if uuidPattern.MatchString(ref) {
		return ref, nil
	}
	body, err := s.get(ctx, s.orgPath("spaces/"+spaceID+"/agents"), nil)
	if err != nil {
		return "", err
	}
	value, err := decodeObject(body)
	if err != nil {
		return "", err
	}
	return resolveByName(objectsAt(value, "agents"), "name", ref, "agent")
}

func (s *cliSession) meID(ctx context.Context) (string, error) {
	body, err := s.get(ctx, "/v1/me", nil)
	if err != nil {
		return "", err
	}
	value, err := decodeObject(body)
	if err != nil {
		return "", err
	}
	return field(value, "id"), nil
}

// parseActor 解析 "user:<uuid|me>"、"agent:<uuid|名称>"、"crew:<uuid>"，返回小写 kind 与 ID。
func (s *cliSession) parseActor(ctx context.Context, spaceID, ref string) (string, string, error) {
	kind, id, ok := strings.Cut(ref, ":")
	kind = strings.ToLower(kind)
	if !ok || id == "" || (kind != "user" && kind != "agent" && kind != "crew") {
		return "", "", usageError("%q is not valid; use user:<id|me>, agent:<id|name> or crew:<id>", ref)
	}
	switch {
	case kind == "user" && strings.EqualFold(id, "me"):
		id2, err := s.meID(ctx)
		return kind, id2, err
	case kind == "agent":
		id2, err := s.resolveAgent(ctx, spaceID, id)
		return kind, id2, err
	}
	if !uuidPattern.MatchString(id) {
		return "", "", usageError("%q is not a valid %s id", id, kind)
	}
	return kind, id, nil
}

// readBody 取正文：--body 优先，否则读 stdin；空正文报参数错误。
func (c *cli) readBody(flagValue string, flagSet bool) (string, error) {
	text := flagValue
	if !flagSet {
		data, err := io.ReadAll(io.LimitReader(c.stdin, maxStdinBytes+1))
		if err != nil {
			return "", err
		}
		if len(data) > maxStdinBytes {
			return "", usageError("body from stdin is too large (limit 64 KiB)")
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return "", usageError("body is empty; pass --body <text> or pipe the text on stdin")
	}
	return text, nil
}

// visited 返回命令行里显式设置过的 flag 名。
func visited(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

func jsonMarshalIndent(value any) ([]byte, error) { return json.MarshalIndent(value, "", "  ") }
