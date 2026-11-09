// 本机模拟的腾讯 ClawBot（iLink），供浏览器联调使用：
//
//	go run ./tests/support/fakewechat serve -addr 127.0.0.1:18101 -user wxid_demo@im.wechat
//
// 联调时把服务端的 OPENFOX_WECHAT_ILINK_BASE_URL 设为 http://127.0.0.1:18101（development / test 环境允许 http，
// 且允许 confirmed 返回的 baseurl 等于该配置基址）。每个新二维码按 wait → scaned → confirmed 推进（每次状态查询一步），
// confirmed 时返回 -user 指定的微信用户。管理接口：
//
//	POST /_fake/push     {"token","from_user_id","text","group_id","message_type","no_text","context_token"}  模拟微信用户发消息
//	GET  /_fake/messages                                                                                      bot 发出的消息
//	POST /_fake/expire   {"token"}                                                                            让该 bot 的会话失效（-14）
//
// token 是 confirmed 时生成的 tok_<二维码标识>（日志里可见）。绝不访问真实的 ilinkai.weixin.qq.com。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	fake "openfox/tests/support/fakewechat/fake"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: fakewechat serve -addr 127.0.0.1:18101 -user wxid_demo@im.wechat")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := flags.String("addr", "127.0.0.1:18101", "监听地址")
	user := flags.String("user", "wxid_demo@im.wechat", "扫码确认的微信用户（ilink_user_id）")
	hold := flags.Duration("hold", 5*time.Second, "长轮询无新内容时的服务端等待时长")
	_ = flags.Parse(os.Args[2:])
	server := fake.NewServer()
	server.URL = "http://" + *addr
	server.Hold = *hold
	server.SetDefaultPlan(func(code string) []fake.QRStep {
		return []fake.QRStep{{Status: "wait"}, {Status: "scaned"}, {Status: "confirmed",
			BotToken: "tok_" + code, BotID: "bot_" + code + "@im.bot", UserID: *user, BaseURL: server.URL}}
	})
	log.Printf("fakewechat listening on http://%s (tokens are tok_<qrcode>)", *addr)
	httpServer := &http.Server{Addr: *addr, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(httpServer.ListenAndServe())
}
