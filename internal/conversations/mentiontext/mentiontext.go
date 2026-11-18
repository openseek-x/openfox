// Package mentiontext 解析评论正文里的提及令牌。
//
// 正文仍是 Markdown，提及写成令牌（名字里的 [ ] 与换行由客户端去掉）：
//
//	@[名字](openfox://mention/user/<uuid>)
//	@[名字](openfox://mention/agent/<uuid>)
//	@[名字](openfox://mention/crew/<uuid>)
//	@[全体成员](openfox://mention/all)
//
// 令牌只负责显示；提及以请求里的 mentions 数组为准，服务端用 Parse 校验两者一致。
package mentiontext

import (
	"regexp"
	"strings"
)

const (
	KindUser  = "user"
	KindAgent = "agent"
	KindCrew  = "crew"
	KindAll   = "all"
)

// Token 是正文里解析出的一个提及令牌。ID 对 all 为空。
type Token struct {
	Kind string
	ID   string
	Name string
}

var tokenPattern = regexp.MustCompile(
	`@\[([^\[\]\r\n]*)\]\(openfox://mention/(?:(user|agent|crew)/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})|(all))\)`)

// Parse 按出现顺序返回正文里的全部令牌（ID 统一为小写）。
func Parse(body string) []Token {
	matches := tokenPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	tokens := make([]Token, 0, len(matches))
	for _, match := range matches {
		if match[4] != "" {
			tokens = append(tokens, Token{Kind: KindAll, Name: match[1]})
			continue
		}
		tokens = append(tokens, Token{Kind: match[2], ID: strings.ToLower(match[3]), Name: match[1]})
	}
	return tokens
}

// Plain 把令牌替换成 @名字，其余文本原样保留；用于通知摘要、邮件与 IM 正文、外部评论出站。
func Plain(body string) string {
	if !strings.Contains(body, "openfox://mention/") {
		return body
	}
	return tokenPattern.ReplaceAllString(body, "@$1")
}
