//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package runtime

import (
	"context"
	"syscall"
	"testing"
	"time"
)

func TestTerminateProcessEscalatesToKill(t *testing.T) {
	previousInterrupt, previousTerminate := interruptGrace, terminateGrace
	interruptGrace, terminateGrace = 50*time.Millisecond, 50*time.Millisecond
	defer func() { interruptGrace, terminateGrace = previousInterrupt, previousTerminate }()

	// 进程忽略 SIGINT 与 SIGTERM，只能被 SIGKILL 结束。
	path := writeRuntimeFixture(t, `#!/bin/sh
trap '' INT TERM
printf '%s\n' '{"type":"started"}'
while :; do sleep 1; done
`)
	exec, err := StartProcess(context.Background(), ProcessSpec{
		Executable: path, Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin"},
		Timeout: 10 * time.Second, MaxLineBytes: 1024, MaxOutputBytes: 4096, MaxEvents: 10,
		DecodeLine: decodeTestEvent,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-exec.Events()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if err := exec.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	result, err := exec.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCancelled || !result.ProcessExited {
		t.Fatalf("result = %#v", result)
	}
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond {
		t.Fatalf("process stopped after %s, expected escalation through both grace periods", elapsed)
	}
}

// TestStartProcessKeepsTailLinesAfterFastExit 回归管道竞争：进程写完最后几行立即退出时，
// cmd.Wait 不能先关闭读端导致 completed 丢失（原表现为偶发 PROTOCOL_ERROR）。
func TestStartProcessKeepsTailLinesAfterFastExit(t *testing.T) {
	path := writeRuntimeFixture(t, `#!/bin/sh
i=0
while [ $i -lt 60 ]; do printf '%s\n' '{"type":"output"}'; i=$((i+1)); done
printf '%s\n' '{"type":"completed"}'
`)
	// 慢解码让读取协程积压：进程早已退出，剩余行仍留在管道缓冲中。
	slowDecode := func(line []byte) (Event, error) {
		time.Sleep(2 * time.Millisecond)
		return decodeTestEvent(line)
	}
	for attempt := 0; attempt < 5; attempt++ {
		exec, err := StartProcess(context.Background(), ProcessSpec{
			Executable: path, Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin"},
			Timeout: 10 * time.Second, MaxLineBytes: 1024, MaxOutputBytes: 64 * 1024, MaxEvents: 100,
			RequireCompletion: true, DecodeLine: slowDecode,
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := exec.Wait(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != StatusSucceeded || result.EventCount != 61 {
			t.Fatalf("attempt %d: status = %s (%s), events = %d", attempt, result.Status, result.Error, result.EventCount)
		}
	}
}

// TestStartProcessDoesNotHangOnInheritedPipe 覆盖后台子进程继承输出管道的情况：
// 主进程退出后最多等待 pipeDrainGrace，不能一直等到子进程结束。
func TestStartProcessDoesNotHangOnInheritedPipe(t *testing.T) {
	previous := pipeDrainGrace
	pipeDrainGrace = 200 * time.Millisecond
	defer func() { pipeDrainGrace = previous }()
	path := writeRuntimeFixture(t, `#!/bin/sh
sleep 5 &
printf '%s\n' '{"type":"completed"}'
`)
	started := time.Now()
	exec, err := StartProcess(context.Background(), ProcessSpec{
		Executable: path, Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin"},
		Timeout: 10 * time.Second, MaxLineBytes: 1024, MaxOutputBytes: 4096, MaxEvents: 10,
		RequireCompletion: true, DecodeLine: decodeTestEvent,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 清理遗留的后台 sleep（同一进程组）。
	defer func() { _ = syscall.Kill(-exec.(*processExecution).cmd.Process.Pid, syscall.SIGKILL) }()
	result, err := exec.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSucceeded || !result.ProcessExited {
		t.Fatalf("result = %#v", result)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("waited %s for inherited pipe, want about pipeDrainGrace", elapsed)
	}
}
