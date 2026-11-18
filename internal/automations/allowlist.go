package automations

import (
	"net/netip"
	"strings"
)

// maxIPAllowlist 与契约及迁移的 ip_allowlist 上限一致。
const maxIPAllowlist = 20

// ValidateIPAllowlist 校验白名单：每条必须是 IPv4 / IPv6 地址或 CIDR，数量不超过 20。
func ValidateIPAllowlist(entries []string) error {
	if len(entries) > maxIPAllowlist {
		return invalidf("IP 白名单最多 %d 条", maxIPAllowlist)
	}
	for _, entry := range entries {
		if _, err := parseAllowEntry(entry); err != nil {
			return invalidf("IP 白名单条目无效 %q", entry)
		}
	}
	return nil
}

// IPAllowed 判断来源 IP 是否在白名单内；白名单为空表示不限制。
// 来源 IP 无法解析时视为不在白名单内。
func IPAllowed(allowlist []string, source string) bool {
	if len(allowlist) == 0 {
		return true
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(source))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, entry := range allowlist {
		prefix, err := parseAllowEntry(entry)
		if err != nil {
			continue
		}
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// parseAllowEntry 把单个地址视为单主机前缀，统一用前缀匹配。
func parseAllowEntry(entry string) (netip.Prefix, error) {
	entry = strings.TrimSpace(entry)
	if strings.Contains(entry, "/") {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return netip.Prefix{}, err
		}
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}
