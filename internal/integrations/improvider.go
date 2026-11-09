package integrations

// IM 服务商参数化。Lark 与飞书是同一套开放平台接口，只有品牌词、域名与服务商标识不同：
// 飞书实现里的 SQL 以 'feishu' 字面量为模板，运行时按服务商替换成白名单内的标识；
// 客户端按服务商取（飞书 / Lark 各自的域名）。服务商标识只来自这里的白名单，不接受用户输入。

import "strings"

const (
	ProviderFeishu   = "feishu"
	ProviderLark     = "lark"
	ProviderDingTalk = "dingtalk"
	ProviderWeCom    = "wecom"
)

// feishuFamily 是与飞书共用同一套开放平台协议的服务商（飞书、Lark）。
var feishuFamily = []string{ProviderFeishu, ProviderLark}

// IsIMProvider 判断服务商是否是 IM 服务商（飞书、Lark、钉钉）：它们共用 6d 的长连接状态、通知出站与机器人表。
func IsIMProvider(provider string) bool {
	return IsFeishuFamily(provider) || provider == ProviderDingTalk || provider == ProviderWeCom
}

// IsFeishuFamily 判断服务商是否走飞书协议实现。
func IsFeishuFamily(provider string) bool {
	for _, item := range feishuFamily {
		if item == provider {
			return true
		}
	}
	return false
}

// prov 返回当前服务商标识；零值按飞书处理，保持 6d 的行为不变。
func (s Service) prov() string {
	if s.IM == "" {
		return ProviderFeishu
	}
	return s.IM
}

// For 返回绑定到指定 IM 服务商的 Service 副本：Feishu 字段换成该服务商的客户端（未配置时为 nil）。
func (s Service) For(provider string) Service {
	out := s
	out.IM = provider
	if provider == ProviderDingTalk || provider == ProviderWeCom {
		out.Feishu = nil // 钉钉不走飞书客户端
		return out
	}
	if provider == ProviderFeishu && s.IMClients[provider] == nil {
		return out
	}
	out.Feishu = s.IMClients[provider]
	return out
}

// providerBrand 是服务商在中文提示里的名字。
func providerBrand(provider string) string {
	switch provider {
	case ProviderLark:
		return "Lark"
	case ProviderDingTalk:
		return "钉钉"
	case ProviderWeCom:
		return "企业微信"
	default:
		return "飞书"
	}
}

// forProv 把模板 SQL 里的 'feishu' 字面量换成指定服务商。provider 必须来自白名单；
// 不在白名单内时退回飞书，避免把任意字符串拼进 SQL。
func forProv(query, provider string) string {
	switch provider {
	case ProviderLark, ProviderDingTalk, ProviderWeCom:
		return strings.ReplaceAll(query, "'feishu'", "'"+provider+"'")
	default:
		return query
	}
}

// familyIn 是 SQL 里匹配飞书家族的 IN 列表，供按连接 ID 工作的后台任务使用。
const familyIn = "('feishu', 'lark')"
