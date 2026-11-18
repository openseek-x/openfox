//go:build freebsd || openbsd || netbsd || dragonfly

package runtime

// 其他类 Unix 平台暂不支持读取启动时间：核对时按“无法验证”处理，不会终止任何进程。
func processStartToken(int) (int64, bool) { return 0, false }
