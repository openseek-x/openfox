//go:build linux

package runtime

import (
	"os"
	"strconv"
	"strings"
)

// processStartToken 读取 /proc/<pid>/stat 的第 22 项（自开机起的时钟节拍数）作为启动令牌。
func processStartToken(pid int) (int64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	text := string(data)
	// comm 字段可能含空格和括号，从最后一个 ')' 之后解析。
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(text[end+1:])
	// ')' 之后第 1 项是 state（总第 3 项），starttime 是总第 22 项，即此处下标 19。
	if len(fields) < 20 {
		return 0, false
	}
	token, err := strconv.ParseInt(fields[19], 10, 64)
	return token, err == nil && token > 0
}
