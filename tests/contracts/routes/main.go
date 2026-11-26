// 用真实 ServeMux 匹配展开后的 OpenAPI 方法与路径，再检查源码中直接注册的路由都有契约覆盖。
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"openfox/internal/api"
	"openfox/internal/identity"
)

func main() {
	var operations []string
	if err := json.NewDecoder(os.Stdin).Decode(&operations); err != nil {
		panic(err)
	}
	mux := api.New(identity.Service{}, nil, false).(*http.ServeMux)
	parameter := regexp.MustCompile(`\{[^}]+\}`)
	seen := map[string]bool{}
	var problems []string
	var health = map[string]bool{}
	for _, operation := range operations {
		method, path, _ := strings.Cut(operation, " ")
		if !strings.HasPrefix(path, "/v1/") {
			health[operation] = true
			continue
		}
		req := httptest.NewRequest(method, parameter.ReplaceAllString(path, "example"), nil)
		_, pattern := mux.Handler(req)
		if !strings.HasPrefix(pattern, method+" /v1/") {
			problems = append(problems, "无对应处理函数 "+operation)
			continue
		}
		seen[pattern] = true
	}
	files, err := filepath.Glob("internal/api/*.go")
	if err != nil {
		panic(err)
	}
	files = append(files, "internal/platform/httpserver/server.go")
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			panic(err)
		}
		ast.Inspect(tree, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Handle" && selector.Sel.Name != "HandleFunc") {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			pattern, _ := strconv.Unquote(literal.Value)
			if file == "internal/platform/httpserver/server.go" && strings.HasPrefix(pattern, "GET /") {
				endpoint := strings.TrimSuffix(pattern, "{$}")
				if !health[endpoint] {
					problems = append(problems, "缺少契约 "+endpoint)
				}
				delete(health, endpoint)
			}
			if strings.Contains(pattern, " /v1/") && !seen[pattern] {
				problems = append(problems, "缺少契约 "+pattern)
			}
			return true
		})
	}
	for route := range health {
		problems = append(problems, "无对应处理函数 "+route)
	}
	slices.Sort(problems)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
	fmt.Printf("匹配到 %d 条不同的实际注册路由。\n", len(seen))
}
