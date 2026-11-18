// 本机模拟的 GitLab / 极狐，供浏览器联调使用：
//
//	go run ./tests/support/fakegitlab serve -addr 127.0.0.1:18102 -login lin -groups openfox-dev \
//	    -repos openfox-dev/app:private,openfox-dev/sub/site
//	go run ./tests/support/fakegitlab send -url http://127.0.0.1:8080/api/v1/hooks/git/gitlab/<令牌> -event "Merge Request Hook" -file payload.json
//
// 联调时在 OpenFox 的 GitLab 绑定对话框里填：实例地址 http://127.0.0.1:18102（development / test 环境允许 http 与本机地址）、
// Application ID / Secret 取自 FAKEGITLAB_CLIENT_ID / FAKEGITLAB_CLIENT_SECRET（默认 fake-app-id / fake-app-secret）。
// Webhook 的 Secret token 从 -secret 参数或环境变量 FAKEGITLAB_WEBHOOK_SECRET 读取，不会打印。管理接口：
//
//	POST /_fake/repo        {"path":"openfox-dev/app","private":true}                 增加项目
//	POST /_fake/user        {"id":77,"username":"ming"}                               增加实例用户
//	POST /_fake/issue       {"project","title","description","author_id","author"}    在实例上直接建 Issue（用于导入已有 Issue）
//	GET  /_fake/issues?project=openfox-dev/app                                        项目的 Issue
//	GET  /_fake/notes                                                                 收到的评论（回链等）
//	POST /_fake/revoke      {"all":false}                                             访问令牌失效（all=true 连刷新令牌一起）
//	POST /_fake/expire                                                                访问令牌立即过期（可刷新）
//	POST /_fake/rate-limit  {"seconds":30}                                            API 限流 429（0 解除）
//
// 绝不访问真实的 GitLab 实例。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	fakegitlab "openfox/tests/support/fakegitlab/fake"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "send":
		if err := send(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "send:", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: fakegitlab serve -addr 127.0.0.1:18102 [-login lin] [-groups openfox-dev] [-repos a/b:private,a/c]\n       fakegitlab send -url <webhook> -event <X-Gitlab-Event> -file payload.json")
	os.Exit(2)
}

func serve(args []string) {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := flags.String("addr", "127.0.0.1:18102", "监听地址")
	login := flags.String("login", "lin", "授权者 login")
	groups := flags.String("groups", "openfox-dev", "授权者所属的顶级群组，逗号分隔")
	repos := flags.String("repos", "", "项目，逗号分隔；后缀 :private 表示私有，例如 openfox-dev/app:private,openfox-dev/sub/site")
	ttl := flags.Int("token-ttl", 7200, "访问令牌有效期（秒）")
	_ = flags.Parse(args)
	var list []string
	for _, item := range strings.Split(*groups, ",") {
		if item = strings.TrimSpace(item); item != "" {
			list = append(list, item)
		}
	}
	server := fakegitlab.NewHandler(fakegitlab.Options{ClientID: os.Getenv("FAKEGITLAB_CLIENT_ID"), ClientSecret: os.Getenv("FAKEGITLAB_CLIENT_SECRET"),
		Login: *login, Groups: list, TokenTTL: *ttl})
	for _, item := range strings.Split(*repos, ",") {
		if item = strings.TrimSpace(item); item != "" {
			name, private := strings.CutSuffix(item, ":private")
			server.AddRepo(name, private)
		}
	}
	if pat := os.Getenv("FAKEGITLAB_PERSONAL_TOKEN"); pat != "" {
		server.AddPersonalToken(pat, *login, 42, "api")
	}
	mux := http.NewServeMux()
	mux.Handle("/", server.Handler())
	mountAdmin(mux, server)
	log.Printf("fakegitlab listening on http://%s (login=%s groups=%v)", *addr, *login, list)
	httpServer := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(httpServer.ListenAndServe())
}

func mountAdmin(mux *http.ServeMux, server *fakegitlab.Server) {
	decode := func(w http.ResponseWriter, r *http.Request, target any) bool {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(target); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return false
		}
		return true
	}
	mux.HandleFunc("POST /_fake/repo", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path    string `json:"path"`
			Private bool   `json:"private"`
		}
		if decode(w, r, &in) {
			server.AddRepo(in.Path, in.Private)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /_fake/user", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}
		if decode(w, r, &in) {
			server.AddUser(in.ID, in.Username)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /_fake/issue", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Project     string `json:"project"`
			Title       string `json:"title"`
			Description string `json:"description"`
			AuthorID    int64  `json:"author_id"`
			Author      string `json:"author"`
		}
		if decode(w, r, &in) {
			writeJSON(w, server.AddIssue(in.Project, in.Title, in.Description, in.AuthorID, in.Author))
		}
	})
	mux.HandleFunc("GET /_fake/issues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, server.Issues(r.URL.Query().Get("project")))
	})
	mux.HandleFunc("GET /_fake/notes", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, server.Notes()) })
	mux.HandleFunc("POST /_fake/revoke", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			All bool `json:"all"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&in)
		if in.All {
			server.RevokeAll()
		} else {
			server.RevokeTokens()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /_fake/expire", func(w http.ResponseWriter, _ *http.Request) {
		server.ExpireTokens()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /_fake/rate-limit", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Seconds int `json:"seconds"`
		}
		if decode(w, r, &in) {
			server.SetRateLimit(time.Duration(in.Seconds) * time.Second)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func send(args []string) error {
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	target := flags.String("url", "", "Webhook 地址")
	secret := flags.String("secret", "", "Secret token（也可用环境变量 FAKEGITLAB_WEBHOOK_SECRET）")
	event := flags.String("event", "Merge Request Hook", "X-Gitlab-Event")
	delivery := flags.String("delivery", "", "Idempotency-Key，默认按时间生成")
	file := flags.String("file", "", "请求体 JSON 文件")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *target == "" || *file == "" {
		return fmt.Errorf("-url and -file are required")
	}
	if *secret == "" {
		*secret = os.Getenv("FAKEGITLAB_WEBHOOK_SECRET")
	}
	if *delivery == "" {
		*delivery = fmt.Sprintf("fake-%d", time.Now().UnixNano())
	}
	payload, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(fakegitlab.SignedWebhook(*target, *secret, *event, *delivery, payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	fmt.Printf("%d %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
	return nil
}
