// 本机模拟的 Gitee，供浏览器联调使用：
//
//	go run ./tests/support/fakegitee serve -addr 127.0.0.1:18103 -login lin -orgs openfox-dev \
//	    -repos openfox-dev/app:private,openfox-dev/site
//	go run ./tests/support/fakegitee send -url http://127.0.0.1:8080/api/v1/hooks/git/gitee/<令牌> -event "Merge Request Hook" \
//	    -mode password|sign -file payload.json
//
// 联调时服务端需要设置（development / test 环境允许 http 与本机地址）：
//
//	OPENFOX_GITEE_CLIENT_ID=fake-gitee-id（或 FAKEGITEE_CLIENT_ID 的值）
//	OPENFOX_GITEE_CLIENT_SECRET_FILE=<绝对路径，内容 fake-gitee-secret（或 FAKEGITEE_CLIENT_SECRET 的值），权限 0600>
//	OPENFOX_GITEE_BASE_URL=http://127.0.0.1:18103
//	OPENFOX_GITEE_API_URL=http://127.0.0.1:18103/api/v5
//
// Webhook 的密码 / 签名密钥从 -secret 参数或环境变量 FAKEGITEE_WEBHOOK_SECRET 读取，不会打印。管理接口：
//
//	POST /_fake/repo        {"path":"openfox-dev/app","private":true}              增加仓库
//	POST /_fake/user        {"id":77,"login":"ming"}                               增加用户
//	POST /_fake/issue       {"repo","title","body","author_id","author"}           直接建 Issue（用于导入已有 Issue）
//	GET  /_fake/issues?repo=openfox-dev/app                                        仓库的 Issue
//	GET  /_fake/notes                                                              收到的评论（回链等）
//	POST /_fake/revoke      {"all":false}                                          访问令牌失效（all=true 连刷新令牌一起）
//	POST /_fake/expire                                                             访问令牌立即过期（可刷新）
//	POST /_fake/rate-limit  {"seconds":30}                                         API 限流 429（0 解除）
//
// 绝不访问真实的 Gitee。
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

	fakegitee "openfox/tests/support/fakegitee/fake"
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
	fmt.Fprintln(os.Stderr, "usage: fakegitee serve -addr 127.0.0.1:18103 [-login lin] [-orgs openfox-dev] [-repos a/b:private,a/c]\n       fakegitee send -url <webhook> -event <X-Gitee-Event> -mode password|sign -file payload.json")
	os.Exit(2)
}

func serve(args []string) {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := flags.String("addr", "127.0.0.1:18103", "监听地址")
	login := flags.String("login", "lin", "授权者 login")
	orgs := flags.String("orgs", "openfox-dev", "授权者所属的组织，逗号分隔")
	repos := flags.String("repos", "", "仓库，逗号分隔；后缀 :private 表示私有，例如 openfox-dev/app:private,openfox-dev/site")
	ttl := flags.Int("token-ttl", 86400, "访问令牌有效期（秒）")
	_ = flags.Parse(args)
	var list []string
	for _, item := range strings.Split(*orgs, ",") {
		if item = strings.TrimSpace(item); item != "" {
			list = append(list, item)
		}
	}
	server := fakegitee.NewHandler(fakegitee.Options{ClientID: os.Getenv("FAKEGITEE_CLIENT_ID"), ClientSecret: os.Getenv("FAKEGITEE_CLIENT_SECRET"),
		Login: *login, Orgs: list, TokenTTL: *ttl})
	for _, item := range strings.Split(*repos, ",") {
		if item = strings.TrimSpace(item); item != "" {
			name, private := strings.CutSuffix(item, ":private")
			server.AddRepo(name, private)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/", server.Handler())
	mountAdmin(mux, server)
	log.Printf("fakegitee listening on http://%s (login=%s orgs=%v)", *addr, *login, list)
	httpServer := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(httpServer.ListenAndServe())
}

func mountAdmin(mux *http.ServeMux, server *fakegitee.Server) {
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
			ID    int64  `json:"id"`
			Login string `json:"login"`
		}
		if decode(w, r, &in) {
			server.AddUser(in.ID, in.Login)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /_fake/issue", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Repo     string `json:"repo"`
			Title    string `json:"title"`
			Body     string `json:"body"`
			AuthorID int64  `json:"author_id"`
			Author   string `json:"author"`
		}
		if decode(w, r, &in) {
			writeJSON(w, server.AddIssue(in.Repo, in.Title, in.Body, in.AuthorID, in.Author))
		}
	})
	mux.HandleFunc("GET /_fake/issues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, server.Issues(r.URL.Query().Get("repo")))
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
	secret := flags.String("secret", "", "密码 / 签名密钥（也可用环境变量 FAKEGITEE_WEBHOOK_SECRET）")
	event := flags.String("event", "Merge Request Hook", "X-Gitee-Event")
	mode := flags.String("mode", "password", "password（明文密码）或 sign（签名密钥）")
	file := flags.String("file", "", "请求体 JSON 文件")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *target == "" || *file == "" {
		return fmt.Errorf("-url and -file are required")
	}
	if *secret == "" {
		*secret = os.Getenv("FAKEGITEE_WEBHOOK_SECRET")
	}
	payload, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var req *http.Request
	switch *mode {
	case "password":
		req = fakegitee.PasswordWebhook(*target, *secret, *event, time.Now(), payload)
	case "sign":
		req = fakegitee.SignedWebhook(*target, *secret, *event, time.Now(), true, payload)
	default:
		return fmt.Errorf("-mode must be password or sign")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	fmt.Printf("%d %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
	return nil
}
