// 本机模拟的 GitHub，供浏览器联调使用：
//
//	go run ./tests/support/fakegithub -listen 127.0.0.1:18096 -orgs openfox-dev -login lin
//	go run ./tests/support/fakegithub send -url http://127.0.0.1:8080/api/v1/hooks/git/github/<令牌> -event pull_request -file payload.json
//
// 签名密钥从 -secret 参数或环境变量 FAKEGITHUB_WEBHOOK_SECRET 读取，不会打印。
// OAuth 的 client id / secret 取自 FAKEGITHUB_CLIENT_ID / FAKEGITHUB_CLIENT_SECRET（默认 fake-client-id / fake-client-secret）。
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	fakegithub "openfox/tests/support/fakegithub/fake"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "send" {
		if err := send(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "send:", err)
			os.Exit(1)
		}
		return
	}
	listen := flag.String("listen", "127.0.0.1:18096", "监听地址")
	orgs := flag.String("orgs", "openfox-dev", "授权者所属组织，逗号分隔")
	login := flag.String("login", "lin", "授权者 login")
	repos := flag.String("repos", "", "仓库，逗号分隔；后缀 :private 表示私有，例如 openfox-dev/app:private,openfox-dev/site")
	flag.Parse()
	var list []string
	for _, item := range strings.Split(*orgs, ",") {
		if item = strings.TrimSpace(item); item != "" {
			list = append(list, item)
		}
	}
	server := fakegithub.NewHandler(fakegithub.Options{ClientID: os.Getenv("FAKEGITHUB_CLIENT_ID"),
		ClientSecret: os.Getenv("FAKEGITHUB_CLIENT_SECRET"), Login: *login, Orgs: list})
	for _, item := range strings.Split(*repos, ",") {
		if item = strings.TrimSpace(item); item != "" {
			name, private := strings.CutSuffix(item, ":private")
			server.AddRepo(name, private)
		}
	}
	log.Printf("fakegithub listening on http://%s (login=%s orgs=%v)", *listen, *login, list)
	httpServer := &http.Server{Addr: *listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(httpServer.ListenAndServe())
}

func send(args []string) error {
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	target := flags.String("url", "", "Webhook 地址")
	secret := flags.String("secret", "", "签名密钥（也可用环境变量 FAKEGITHUB_WEBHOOK_SECRET）")
	event := flags.String("event", "pull_request", "X-GitHub-Event")
	delivery := flags.String("delivery", "", "X-GitHub-Delivery，默认按时间生成")
	file := flags.String("file", "", "请求体 JSON 文件")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *target == "" || *file == "" {
		return fmt.Errorf("-url and -file are required")
	}
	if *secret == "" {
		*secret = os.Getenv("FAKEGITHUB_WEBHOOK_SECRET")
	}
	if *delivery == "" {
		*delivery = fmt.Sprintf("fake-%d", time.Now().UnixNano())
	}
	payload, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(fakegithub.SignedWebhook(*target, *secret, *event, *delivery, payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	fmt.Printf("%d %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
	return nil
}
