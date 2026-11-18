package toolbridge

import "strings"

// LoopbackEnvironment keeps this private MCP bridge off upstream system/user
// proxies. Existing exclusions are retained and no website routing is changed.
func LoopbackEnvironment(env []string) []string {
	result := append([]string(nil), env...)
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		index := -1
		value := ""
		for i, entry := range result {
			if strings.HasPrefix(entry, key+"=") {
				index, value = i, strings.TrimPrefix(entry, key+"=")
			}
		}
		if value != "" {
			value += ","
		}
		value = key + "=" + value + "127.0.0.1,localhost,::1"
		if index >= 0 {
			result[index] = value
		} else {
			result = append(result, value)
		}
	}
	return result
}
