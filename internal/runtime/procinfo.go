package runtime

// 进程核对用的跨平台类型。daemon 在启动 Runtime 进程时记录 (pid, 启动时间令牌, 进程组号)，
// 重启后据此判断旧进程是否仍是当初那个进程；只有三者吻合才允许终止，PID 被复用时绝不动手。

// ProcessIdentifier 由能暴露自身 OS 进程号的 Execution 实现（包装类型需要透传）。
type ProcessIdentifier interface {
	ProcessID() int
}

// ProcessIDOf 返回 Execution 的 OS 进程号；无法识别（例如复用已有宿主进程的逻辑会话）时返回 0。
func ProcessIDOf(execution Execution) int {
	if typed, ok := execution.(ProcessIdentifier); ok {
		return typed.ProcessID()
	}
	return 0
}

// ProcessProbe 是对某个进程号的一次观察。
type ProcessProbe struct {
	// Exists 表示该进程号当前存在（含无权限发信号但确实存在的情况）。
	Exists bool
	// StartKnown 表示取得了启动时间令牌；StartToken 只能与同一平台上同类令牌比较，不是墙钟时间。
	StartKnown bool
	StartToken int64
}

// ProcessIdentity 是启动时记录的进程身份。
type ProcessIdentity struct {
	PID        int
	StartToken int64
	PGID       int
}

// TerminateOutcome 描述一次按身份终止的结果。
type TerminateOutcome string

const (
	// TerminateGone 表示进程组已不存在（已终止或原本就没有）。
	TerminateGone TerminateOutcome = "GONE"
	// TerminateMismatch 表示进程号存在但身份对不上（PID 被复用），没有发送任何信号。
	TerminateMismatch TerminateOutcome = "MISMATCH"
	// TerminateUnverifiable 表示无法取得启动时间，没有发送任何信号。
	TerminateUnverifiable TerminateOutcome = "UNVERIFIABLE"
	// TerminateFailed 表示发送了信号但进程组仍然存在。
	TerminateFailed TerminateOutcome = "FAILED"
)
