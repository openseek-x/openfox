// 本机模拟的钉钉，供浏览器联调使用：
//
//	go run ./tests/support/fakedingtalk serve -addr 127.0.0.1:18099 -apps dingxxxx:secret -users ou_1:un_1:staff1:张三
//
// 联调时把服务端的 OPENFOX_DINGTALK_API_URL、OPENFOX_DINGTALK_OAPI_URL、OPENFOX_DINGTALK_LOGIN_URL 都指向 http://127.0.0.1:18099。
// 管理接口：POST /_fake/push（{"app_key","payload"}，模拟用户给机器人发消息，payload 为机器人消息回调体）、
// GET /_fake/messages?app_key=（机器人发出的消息）。绝不访问真实的钉钉服务。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	fake "openfox/tests/support/fakedingtalk/fake"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: fakedingtalk serve -addr 127.0.0.1:18099 -apps appKey:secret,... -users openID:unionID:staffID:姓名,...")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := flags.String("addr", "127.0.0.1:18099", "监听地址")
	apps := flags.String("apps", "", "应用，逗号分隔：appKey:secret")
	users := flags.String("users", "", "员工，逗号分隔：openID:unionID:staffID:姓名；第一个员工是授权页自动同意的用户")
	_ = flags.Parse(os.Args[2:])
	server := fake.NewHandler()
	server.URL = "http://" + *addr
	for _, item := range strings.Split(*apps, ",") {
		if parts := strings.SplitN(strings.TrimSpace(item), ":", 2); len(parts) == 2 {
			server.AddApp(parts[0], parts[1])
		}
	}
	first := true
	for _, item := range strings.Split(*users, ",") {
		if parts := strings.SplitN(strings.TrimSpace(item), ":", 4); len(parts) == 4 {
			server.AddUser(fake.User{OpenID: parts[0], UnionID: parts[1], StaffID: parts[2], Name: parts[3]})
			if first {
				server.SetAuthorizeUser(parts[2])
				first = false
			}
		}
	}
	log.Printf("fakedingtalk listening on http://%s", *addr)
	httpServer := &http.Server{Addr: *addr, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(httpServer.ListenAndServe())
}
