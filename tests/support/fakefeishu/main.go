// 本机模拟的飞书，供浏览器联调使用：
//
//	go run ./tests/support/fakefeishu serve -addr 127.0.0.1:18097 -apps cli_xxx:secret:机器人名 -users cli_xxx:ou_xxx:张三
//
// 联调时把服务端的 OPENFOX_FEISHU_BASE_URL 与 OPENFOX_FEISHU_ACCOUNTS_URL 都指向 http://127.0.0.1:18097。
// 管理接口：POST /_fake/push（{"app_id","event_type","event_id"?,"event"}，返回客户端的回包）、GET /_fake/messages?app_id=（已收到的卡片）。
// 6d-2：-unions 按 union_id 登记用户；GET /_fake/registrations（待确认的扫码会话）、POST /_fake/push-message
// （{"app_id","chat_id","chat_type","sender_union_id","text","mention_bot"}，模拟用户给机器人发消息）。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	fakefeishu "openfox/tests/support/fakefeishu/fake"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: fakefeishu serve -addr 127.0.0.1:18097 -apps appID:secret:botName,... -users appID:openID:name,... -unions appID:unionID:name,...")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := flags.String("addr", "127.0.0.1:18097", "监听地址")
	brand := flags.String("brand", "feishu", "扫码租户品牌 feishu / lark")
	apps := flags.String("apps", "", "应用，逗号分隔：appID:secret:机器人名称")
	users := flags.String("users", "", "用户，逗号分隔：appID:openID:姓名；第一个用户是授权页自动同意的用户")
	unions := flags.String("unions", "", "按 union_id 登记的用户（6d-2，机器人按 union_id 识别发送人），逗号分隔：appID:unionID:姓名；第一个用户是授权页 / 扫码页自动同意的用户，优先于 -users")
	_ = flags.Parse(os.Args[2:])
	server := fakefeishu.NewHandler()
	server.SetRegistrationBrand(*brand)
	for _, item := range strings.Split(*apps, ",") {
		if parts := strings.SplitN(strings.TrimSpace(item), ":", 3); len(parts) == 3 {
			server.AddApp(parts[0], parts[1], parts[2])
		}
	}
	first := true
	for _, item := range strings.Split(*users, ",") {
		if parts := strings.SplitN(strings.TrimSpace(item), ":", 3); len(parts) == 3 {
			server.AddUser(parts[0], parts[1], parts[2])
			if first {
				server.SetAuthorizeUser(parts[1])
				first = false
			}
		}
	}
	firstUnion := true
	for _, item := range strings.Split(*unions, ",") {
		if parts := strings.SplitN(strings.TrimSpace(item), ":", 3); len(parts) == 3 {
			server.AddUnionUser(parts[0], parts[1], parts[2])
			if firstUnion {
				server.SetAuthorizeUnion(parts[1])
				firstUnion = false
			}
		}
	}
	log.Printf("fakefeishu listening on http://%s", *addr)
	httpServer := &http.Server{Addr: *addr, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(httpServer.ListenAndServe())
}
