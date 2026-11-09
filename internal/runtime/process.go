package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type ProcessSpec struct {
	Executable        string
	Args              []string
	Directory         string
	Environment       []string
	Timeout           time.Duration
	MaxLineBytes      int
	MaxOutputBytes    int
	MaxEvents         int
	RequireCompletion bool
	DecodeLine        func([]byte) (Event, error)
	Redact            func(string) string
	// SummarizeStderr extracts a provider's user-facing diagnostic from log metadata.
	SummarizeStderr func(string) string
	// Session 非空时启用双向模式（打开 stdin），逐行处理交给 Session，DecodeLine 不再使用。
	Session Session
	// CloseStdin 为 true 时，Session 模式下把 StdinOnce（可为空）原样写入 stdin 后关闭 stdin，
	// 之后不再写入；用于从 stdin 读取提示的 CLI（避开命令行参数长度限制）。
	StdinOnce  []byte
	CloseStdin bool
}

// Sender 向 Runtime 的 stdin 写入一条消息（自动追加换行）。并发安全，单次写入有超时。
type Sender func(message []byte) error

// Session 描述基于 stdin/stdout 的双向行协议，例如 Codex app-server 的 JSON-RPC。
// Begin、HandleLine、Interrupt 都在进程监督协程中串行调用。
type Session interface {
	// Begin 在进程启动后调用一次，用于发送初始请求。fail 可在任意协程中调用，
	// 以失败结束本次执行（例如握手超时）。
	Begin(send Sender, fail func(error)) error
	// HandleLine 处理一行 stdout，返回要发布的事件。done=true 表示会话已有终态，
	// 进程层随后关闭 stdin 并等待进程退出。
	HandleLine(line []byte, send Sender) (events []Event, done bool, err error)
	// Interrupt 在用户取消时调用，请求 Runtime 自行停止当前工作；返回 false 时直接发信号。
	Interrupt(send Sender) bool
}

// FlushingSession 是可选扩展：会话缓冲增量（如命令输出）时，进程层每 SessionFlushInterval
// 调用一次 Flush，把到期的缓冲作为事件发出。与其他方法一样在监督协程中串行调用。
type FlushingSession interface {
	Flush(now time.Time) []Event
}

// UsageReporter 是可选扩展：执行结束时进程层把会话报告的 token 用量写入 Result.Usage。
type UsageReporter interface {
	Usage() *Usage
}

// FinalizingSession 是可选扩展：stdout 读完、进程退出后调用一次 Finalize，返回的事件按普通事件发布。
// stopped 为 "" 表示正常退出（用于没有显式终态事件的协议），"cancelled" / "timeout" 表示被取消或超时终止
// （用于把仍在运行的步骤收尾）；协议错误或限制导致的失败不调用。
type FinalizingSession interface {
	Finalize(stopped string) []Event
}

// SessionIDReporter 是可选扩展：执行结束时进程层把会话报告的 Runtime 会话 ID 写入 Result.SessionID。
type SessionIDReporter interface {
	SessionID() string
}

// SessionFlushInterval 是调用 FlushingSession.Flush 的周期。
var SessionFlushInterval = 250 * time.Millisecond

// 双向模式的宽限期，定义为变量以便测试缩短。
var (
	// sessionExitGrace：会话已有终态并关闭 stdin 后，等待进程自行退出的上限。
	sessionExitGrace = 5 * time.Second
	// sessionInterruptGrace：发出协议层中断后，等待 Runtime 自行收尾的上限，之后发信号。
	sessionInterruptGrace = 5 * time.Second
	// stdinWriteTimeout：单条消息写入 stdin 的上限，防止 Runtime 不读 stdin 时卡死监督协程。
	stdinWriteTimeout = 5 * time.Second
)

func StartProcess(ctx context.Context, spec ProcessSpec) (Execution, error) {
	if err := validateProcessSpec(spec); err != nil {
		return nil, err
	}
	if spec.Redact == nil {
		values := make(map[string]string, len(spec.Environment))
		for _, entry := range spec.Environment {
			key, value, _ := strings.Cut(entry, "=")
			values[key] = value
		}
		spec.Redact = redactValues(values)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Dir = spec.Directory
	cmd.Env = append([]string(nil), spec.Environment...)
	configureProcessGroup(cmd)
	// 自建管道而不用 StdoutPipe：cmd.Wait 会关闭 StdoutPipe 的读端，
	// 进程写完最后几行立即退出时，未读的行（如 turn.completed）会丢失。
	// 交给子进程的是 *os.File 写端，Wait 不会关闭我们持有的读端。
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("runtime: create stdout pipe: %w", err)
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return nil, fmt.Errorf("runtime: create stderr pipe: %w", err)
	}
	cmd.Stdout, cmd.Stderr = stdoutWriter, stderrWriter
	// 双向模式：子进程持有 stdin 读端，我们持有写端；非双向模式 stdin 为 /dev/null。
	var stdinReader, stdinWriter *os.File
	if spec.Session != nil {
		stdinReader, stdinWriter, err = os.Pipe()
		if err != nil {
			for _, file := range []*os.File{stdout, stdoutWriter, stderr, stderrWriter} {
				_ = file.Close()
			}
			return nil, fmt.Errorf("runtime: create stdin pipe: %w", err)
		}
		cmd.Stdin = stdinReader
	}
	startErr := cmd.Start()
	// 父进程不再需要子进程一侧的管道端；关闭后，所有持有写端的进程退出时读端得到 EOF。
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	if stdinReader != nil {
		_ = stdinReader.Close()
	}
	if startErr != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		if stdinWriter != nil {
			_ = stdinWriter.Close()
		}
		return nil, fmt.Errorf("%w: start %s: %v", ErrUnavailable, spec.Executable, safeError(startErr))
	}
	exec := &processExecution{
		cmd: cmd, spec: spec, waitProcess: cmd.Wait, events: make(chan Event, 128), done: make(chan struct{}),
		startedAt: time.Now().UTC(), stopCh: make(chan stopRequest, 1), stdin: stdinWriter,
	}
	exec.stopProcess = func() { terminateProcess(cmd, exec.done) }
	go exec.run(stdout, stderr)
	return exec, nil
}

func validateProcessSpec(spec ProcessSpec) error {
	if strings.TrimSpace(spec.Executable) == "" || (spec.DecodeLine == nil && spec.Session == nil) || spec.Directory == "" {
		return ErrInvalidRequest
	}
	if !isAbsolutePath(spec.Executable) {
		return fmt.Errorf("%w: executable must be absolute", ErrInvalidRequest)
	}
	if spec.Timeout <= 0 || spec.Timeout > MaxRunTimeout {
		return fmt.Errorf("%w: invalid process timeout", ErrInvalidRequest)
	}
	if spec.MaxLineBytes < 1 || spec.MaxLineBytes > DefaultMaxLineBytes || spec.MaxOutputBytes < 1 || spec.MaxOutputBytes > DefaultMaxOutput || spec.MaxEvents < 1 || spec.MaxEvents > DefaultMaxEvents {
		return fmt.Errorf("%w: invalid process limits", ErrInvalidRequest)
	}
	if err := validateEnvironment(spec.Environment); err != nil {
		return err
	}
	return nil
}

func validateEnvironment(environment []string) error {
	seen := make(map[string]struct{}, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !validEnvName(key) {
			return fmt.Errorf("%w: invalid process environment", ErrInvalidRequest)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate process environment key", ErrInvalidRequest)
		}
		seen[key] = struct{}{}
	}
	return nil
}

type stopKind uint8

const (
	stopCancel stopKind = iota + 1
	stopTimeout
	stopFailure
)

type stopRequest struct {
	kind stopKind
	err  error
}

type lineResult struct {
	stdout bool
	line   []byte
	err    error
}

type processExecution struct {
	cmd         *exec.Cmd // nil for a logical session on an existing host
	waitProcess func() error
	stopProcess func()
	transport   *SessionTransport
	spec        ProcessSpec
	events      chan Event
	done        chan struct{}
	stopCh      chan stopRequest

	startedAt time.Time
	mu        sync.RWMutex
	result    Result
	stopOnce  sync.Once
	stopped   bool

	// stdin 仅在双向模式下非空；stdinMu 串行化写入与关闭。
	stdinMu sync.Mutex
	stdin   *os.File
}

// send 是双向模式的 Sender 实现。
func (e *processExecution) send(message []byte) error {
	if e.transport != nil {
		return e.transport.Send(message)
	}
	if bytes.ContainsAny(message, "\r\n") {
		return fmt.Errorf("%w: session message contains a line break", ErrProtocol)
	}
	e.stdinMu.Lock()
	defer e.stdinMu.Unlock()
	if e.stdin == nil {
		return fmt.Errorf("%w: runtime stdin is closed", ErrProtocol)
	}
	_ = e.stdin.SetWriteDeadline(time.Now().Add(stdinWriteTimeout))
	if _, err := e.stdin.Write(append(append([]byte(nil), message...), '\n')); err != nil {
		return fmt.Errorf("%w: write runtime stdin: %v", ErrProtocol, safeError(err))
	}
	return nil
}

// writeOnceAndClose 把 data 原样写入 stdin（带写超时）后关闭 stdin。
func (e *processExecution) writeOnceAndClose(data []byte) {
	e.stdinMu.Lock()
	defer e.stdinMu.Unlock()
	if e.stdin == nil {
		return
	}
	if len(data) > 0 {
		_ = e.stdin.SetWriteDeadline(time.Now().Add(stdinWriteTimeout))
		_, _ = e.stdin.Write(data)
	}
	_ = e.stdin.Close()
	e.stdin = nil
}

// closeStdin 关闭 stdin，告诉 Runtime 不会再有请求；可重复调用。
func (e *processExecution) closeStdin() {
	if e.transport != nil {
		e.transport.Close()
		return
	}
	e.stdinMu.Lock()
	defer e.stdinMu.Unlock()
	if e.stdin != nil {
		_ = e.stdin.Close()
		e.stdin = nil
	}
}

func (e *processExecution) Events() <-chan Event { return e.events }

func (e *processExecution) Recovery() Recovery {
	return Recovery{Status: RecoveryUnsupported, Reason: "runtime session resume is not verified; reconcile process facts before retry"}
}

func (e *processExecution) Wait(ctx context.Context) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-e.done:
		e.mu.RLock()
		defer e.mu.RUnlock()
		return e.result, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (e *processExecution) Cancel(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.RLock()
	finished := e.stopped
	e.mu.RUnlock()
	if finished {
		return nil
	}
	e.requestStop(stopRequest{kind: stopCancel})
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *processExecution) requestStop(request stopRequest) {
	e.stopOnce.Do(func() { e.stopCh <- request })
}

// pipeDrainGrace 是进程退出后等待输出读完的上限。后台子进程可能继承并一直持有输出管道，
// 超过该时长后关闭读端，已读到的行保留。定义为变量以便测试缩短。
var pipeDrainGrace = 2 * time.Second

func (e *processExecution) run(stdout, stderr io.ReadCloser) {
	defer func() {
		_ = stdout.Close()
		_ = stderr.Close()
		e.closeStdin()
	}()
	lines := make(chan lineResult, 16)
	go readLines(stdout, true, e.spec.MaxLineBytes, lines)
	go readLines(stderr, false, e.spec.MaxLineBytes, lines)
	waitCh := make(chan error, 1)
	go func() { waitCh <- e.waitProcess() }()
	timer := time.NewTimer(e.spec.Timeout)
	defer timer.Stop()

	var waitErr error
	var waitDone, stdoutDone, stderrDone bool
	var stop *stopRequest
	var decodeErr error
	var eventCount uint64
	var output bytes.Buffer
	var outputBytes int
	var sawFailure bool
	var sawCompleted bool
	var failureText string
	// lastWarning 保留最近一条非致命告警，失败且无其他说明时使用。
	var lastWarning string
	// 双向模式：sessionDone 表示会话已有终态；forcedExit 表示终态后进程未按时退出、由我们结束，
	// 此时退出码不代表 Run 失败。
	var sessionDone, forcedExit bool
	var exitGrace, interruptGrace <-chan time.Time

	// fail 以失败结束执行：记录原因并终止进程，只生效一次。
	fail := func(err error) {
		if decodeErr == nil {
			decodeErr = err
		}
		if stop == nil {
			request := stopRequest{kind: stopFailure, err: err}
			stop = &request
			e.stopOnce.Do(func() {})
			e.stopProcess()
		}
	}
	// handleEvent 处理一个解码后的事件；返回 false 表示执行已因限制或背压而失败。
	handleEvent := func(event Event) bool {
		if event.Critical {
			err := event.CriticalError
			if err == nil {
				err = ErrUnsupportedEvent
			}
			fail(err)
			return false
		}
		eventCount++
		if int(eventCount) > e.spec.MaxEvents {
			fail(ErrEventLimit)
			return false
		}
		event.Sequence = eventCount
		if event.At.IsZero() {
			event.At = time.Now().UTC()
		}
		// 结果输出只收集 Runtime 的回复正文；工具输出、告警等只作为过程事件。
		if event.Type == EventOutput && event.Text != "" {
			text := event.Text
			if e.spec.Redact != nil {
				text = e.spec.Redact(text)
			}
			if output.Len() < e.spec.MaxOutputBytes {
				remaining := e.spec.MaxOutputBytes - output.Len()
				if len(text) > remaining {
					text = text[:remaining]
				}
				output.WriteString(text)
				output.WriteByte('\n')
			}
		}
		if event.Type == EventFailed {
			sawFailure = true
			failureText = event.Text
		}
		if event.Type == EventWarning && event.Text != "" {
			lastWarning = event.Text
			if e.spec.Redact != nil {
				lastWarning = e.spec.Redact(lastWarning)
			}
			if len(lastWarning) > 4096 {
				lastWarning = lastWarning[:4096]
			}
		}
		if event.Type == EventCompleted {
			sawCompleted = true
		}
		if !e.emit(event) {
			fail(ErrEventBackpressure)
			return false
		}
		return true
	}

	if session := e.spec.Session; session != nil {
		// 外部协程只能通过 stopCh 请求失败，避免并发修改监督协程的状态。
		external := func(err error) { e.requestStop(stopRequest{kind: stopFailure, err: err}) }
		if err := session.Begin(e.send, external); err != nil {
			fail(err)
		} else if e.spec.CloseStdin {
			// 在独立协程中写入：提示可能大于管道缓冲，不能阻塞监督协程。
			go e.writeOnceAndClose(e.spec.StdinOnce)
		}
	}

	// flushTick 仅在会话实现 FlushingSession 时启用。
	var flushTick <-chan time.Time
	flusher, _ := e.spec.Session.(FlushingSession)
	if flusher != nil {
		ticker := time.NewTicker(SessionFlushInterval)
		defer ticker.Stop()
		flushTick = ticker.C
	}

	// drain 在进程退出后启动，到期仍未读完时关闭读端。
	var drain <-chan time.Time
	for !(waitDone && stdoutDone && stderrDone) {
		if waitDone && drain == nil {
			drainTimer := time.NewTimer(pipeDrainGrace)
			defer drainTimer.Stop()
			drain = drainTimer.C
		}
		select {
		case now := <-flushTick:
			if decodeErr != nil || sessionDone {
				continue
			}
			for _, event := range flusher.Flush(now) {
				if !handleEvent(event) {
					break
				}
			}
		case <-drain:
			_ = stdout.Close()
			_ = stderr.Close()
		case <-exitGrace:
			exitGrace = nil
			if !waitDone {
				forcedExit = true
				e.stopProcess()
			}
		case <-interruptGrace:
			interruptGrace = nil
			if !waitDone {
				e.stopProcess()
			}
		case request := <-e.stopCh:
			if stop != nil {
				continue
			}
			if request.kind == stopFailure {
				fail(request.err)
				continue
			}
			requestCopy := request
			stop = &requestCopy
			if request.kind == stopCancel && e.spec.Session != nil && !sessionDone && e.spec.Session.Interrupt(e.send) {
				// 先让 Runtime 自行收尾，到期仍未退出再发信号。
				graceTimer := time.NewTimer(sessionInterruptGrace)
				defer graceTimer.Stop()
				interruptGrace = graceTimer.C
				continue
			}
			e.stopProcess()
		case <-timer.C:
			if stop == nil {
				request := stopRequest{kind: stopTimeout, err: context.DeadlineExceeded}
				stop = &request
				e.stopOnce.Do(func() {})
				e.stopProcess()
			}
		case waitErr = <-waitCh:
			waitDone = true
		case line := <-lines:
			if line.err != nil {
				// 读端只会在 drain 宽限到期后被关闭：此时进程已退出，
				// 结果由退出事实决定，关闭本身不是错误。
				if !errors.Is(line.err, os.ErrClosed) {
					fail(line.err)
				}
				if line.stdout {
					stdoutDone = true
				} else {
					stderrDone = true
				}
				continue
			}
			if line.stdout && line.line == nil {
				stdoutDone = true
				continue
			}
			if line.stdout && decodeErr != nil {
				// Drain the pipe after the first fatal protocol/limit error so
				// Wait can collect exit evidence without decoding more input.
				continue
			}
			// 输出上限只约束 stdout（协议数据）；stderr 是诊断信息，只保留有界的尾部说明。
			if line.stdout && line.line != nil {
				outputBytes += len(line.line)
				if outputBytes > e.spec.MaxOutputBytes {
					fail(ErrOutputLimit)
					continue
				}
			}
			if !line.stdout {
				if line.line != nil {
					// stderr is diagnostic only; keep a bounded, redacted tail for failures.
					text := strings.TrimSpace(string(line.line))
					if e.spec.SummarizeStderr != nil {
						text = e.spec.SummarizeStderr(text)
					}
					if e.spec.Redact != nil {
						text = e.spec.Redact(text)
					}
					if len(text) > 4096 {
						text = text[:4096]
					}
					if failureText == "" && text != "" {
						failureText = text
					}
				}
				stderrDone = line.line == nil
				continue
			}
			if session := e.spec.Session; session != nil {
				events, done, err := session.HandleLine(line.line, e.send)
				if err != nil {
					fail(err)
					continue
				}
				for _, event := range events {
					if !handleEvent(event) {
						break
					}
				}
				if done && !sessionDone && decodeErr == nil {
					sessionDone = true
					e.closeStdin()
					graceTimer := time.NewTimer(sessionExitGrace)
					defer graceTimer.Stop()
					exitGrace = graceTimer.C
				}
				continue
			}
			event, err := e.spec.DecodeLine(line.line)
			if err != nil {
				fail(err)
				continue
			}
			handleEvent(event)
		}
	}

	if finalizer, ok := e.spec.Session.(FinalizingSession); ok && (stop == nil || stop.kind != stopFailure) && (stop != nil || decodeErr == nil) {
		reason := ""
		if stop != nil {
			reason = map[stopKind]string{stopCancel: "cancelled", stopTimeout: "timeout"}[stop.kind]
		}
		for _, event := range finalizer.Finalize(reason) {
			if !handleEvent(event) {
				break
			}
		}
	}
	result := Result{
		StartedAt: e.startedAt, FinishedAt: time.Now().UTC(), EventCount: eventCount,
		Output: output.String(), ProcessExited: waitDone, Recovery: e.Recovery(),
	}
	if reporter, ok := e.spec.Session.(UsageReporter); ok {
		result.Usage = reporter.Usage()
	}
	if reporter, ok := e.spec.Session.(SessionIDReporter); ok {
		result.SessionID = reporter.SessionID()
	}
	if e.spec.RequireCompletion && !sawCompleted && stop == nil && decodeErr == nil && (waitErr == nil || forcedExit) && !sawFailure {
		decodeErr = ErrProtocol
	}
	result.ExitCode = exitCode(waitErr)
	switch {
	case stop != nil && stop.kind == stopCancel:
		result.Status = StatusCancelled
		result.ErrorCode = "CANCELLED"
		result.Error = "process cancellation confirmed by exit"
	case stop != nil && stop.kind == stopTimeout:
		result.Status = StatusTimedOut
		result.ErrorCode = "RUN_TIMEOUT"
		result.Error = "process timeout confirmed by exit"
		if failureText != "" {
			result.Error += ": " + failureText
		} else if lastWarning != "" {
			result.Error += ": " + lastWarning
		}
	case decodeErr != nil:
		result.Status = StatusFailed
		result.ErrorCode = errorCode(decodeErr)
		result.Error = safeError(decodeErr)
	case sawFailure || (!forcedExit && (waitErr != nil || result.ExitCode != 0)):
		result.Status = StatusFailed
		result.ErrorCode = "RUNTIME_FAILED"
		result.Error = failureText
		if result.Error == "" {
			// 致命错误前 Runtime 往往先报告告警（如重连耗尽），作为失败说明。
			result.Error = lastWarning
		}
		if result.Error == "" {
			result.Error = safeError(waitErr)
		}
	default:
		result.Status = StatusSucceeded
	}
	if result.Error == "" && result.Status == StatusFailed {
		result.Error = "runtime exited unsuccessfully"
	}
	if result.Status == StatusCancelled {
		e.emit(Event{Type: EventCancelled, Text: result.Error})
	}
	e.mu.Lock()
	e.result = result
	e.stopped = true
	e.mu.Unlock()
	close(e.events)
	close(e.done)
}

func (e *processExecution) emit(event Event) bool {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case e.events <- event:
		return true
	case <-timer.C:
		return false
	}
}

func readLines(reader io.Reader, stdout bool, maxLine int, out chan<- lineResult) {
	defer func() { out <- lineResult{stdout: stdout} }()
	buffer := bufio.NewReaderSize(reader, min(maxLine, 64*1024))
	for {
		var line []byte
		for {
			part, err := buffer.ReadSlice('\n')
			if len(part) > 0 {
				if len(line)+len(part) > maxLine {
					out <- lineResult{stdout: stdout, err: ErrOutputLimit}
					return
				}
				line = append(line, part...)
			}
			switch {
			case err == nil:
				line = bytes.TrimSuffix(line, []byte{'\n'})
				line = bytes.TrimSuffix(line, []byte{'\r'})
				out <- lineResult{stdout: stdout, line: line}
				line = nil
			case errors.Is(err, bufio.ErrBufferFull):
				continue
			case errors.Is(err, io.EOF):
				if len(line) > 0 {
					out <- lineResult{stdout: stdout, line: line}
				}
				return
			default:
				out <- lineResult{stdout: stdout, err: err}
				return
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrOutputLimit):
		return "OUTPUT_LIMIT"
	case errors.Is(err, ErrEventLimit):
		return "EVENT_LIMIT"
	case errors.Is(err, ErrEventBackpressure):
		return "EVENT_BACKPRESSURE"
	case errors.Is(err, ErrUnsupportedEvent):
		return "UNSUPPORTED_EVENT"
	case errors.Is(err, ErrApprovalRequired):
		return "APPROVAL_REQUIRED"
	case errors.Is(err, ErrProtocol):
		return "PROTOCOL_ERROR"
	default:
		return "RUNTIME_ERROR"
	}
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 4096 {
		text = text[:4096]
	}
	return text
}

func redactValues(values map[string]string) func(string) string {
	secrets := make([]string, 0)
	for key, value := range values {
		upper := strings.ToUpper(key)
		if value != "" && (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API_KEY") || strings.Contains(upper, "CREDENTIAL")) {
			secrets = append(secrets, value)
		}
	}
	return func(text string) string {
		for _, secret := range secrets {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
		return text
	}
}

// SessionTransport represents one independently closable connection to a runtime host.
// Stop must terminate active work. Wait must not return before that work has stopped.
type SessionTransport struct {
	Output io.ReadCloser
	Send   Sender
	Close  func()
	Stop   func()
	Wait   func() error
}

func StartConnectedSession(ctx context.Context, spec ProcessSpec, transport *SessionTransport) (Execution, error) {
	if err := validateProcessSpec(spec); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e := &processExecution{spec: spec, transport: transport, waitProcess: transport.Wait, stopProcess: transport.Stop, events: make(chan Event, 128), done: make(chan struct{}), startedAt: time.Now().UTC(), stopCh: make(chan stopRequest, 1)}
	go e.run(transport.Output, io.NopCloser(strings.NewReader("")))
	return e, nil
}
