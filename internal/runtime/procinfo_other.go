//go:build windows || plan9 || wasip1

package runtime

import (
	"context"
	"time"
)

// 执行端只支持 macOS/Unix；这里只保证非 Unix 平台能编译，核对一律视为无法验证。
func InspectProcess(int) ProcessProbe { return ProcessProbe{} }

func CurrentProcessIdentity(pid int) ProcessIdentity { return ProcessIdentity{PID: pid} }

func SetRecoverGraceForTest(_, _, _ time.Duration) func() { return func() {} }

func TerminateRecordedProcess(context.Context, ProcessIdentity) TerminateOutcome {
	return TerminateUnverifiable
}
