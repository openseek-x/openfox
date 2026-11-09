package supervisor

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"openfox/internal/runtime"
)

const (
	// 上传器内存队列上限：中心不可达时超出部分直接写入本地 outbox，事件泵不阻塞
	// （阻塞会让 Runtime 进程层因背压判定失败）。
	maxPendingEvents = 1000
	// 单次上传的重试次数与基础退避。
	eventUploadAttempts = 3
	eventRetryBase      = 200 * time.Millisecond
	// 每次 drain 每个 Run 最多上传的批数，避免一次轮询占用过久。
	outboxDrainBatches = 20
	// Daemon 退出前尽力上传 outbox 的时限。
	outboxShutdownBudget = 10 * time.Second
)

// eventRetryDelay 定义为变量以便测试缩短。
var eventRetryDelay = eventRetryBase

// eventUploader 为单个 Run 上传事件：序号在这里统一分配（1 起连续，supervisor 生成的
// prep_env 步骤与 Runtime 事件共用），批量上传在独立协程进行。上传失败（重试后）写入 journal
// outbox，待下次 flush 时重试；中心永久拒绝（4xx）的批次记录日志后丢弃。
type eventUploader struct {
	run *runState

	mu           sync.Mutex
	sequence     int64
	pending      []EventInput
	pendingBytes int
	closed       bool
	spilled      bool

	wake      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newEventUploader(run *runState) *eventUploader {
	uploader := &eventUploader{run: run, wake: make(chan struct{}, 1), done: make(chan struct{})}
	supervisor := run.supervisor
	supervisor.mu.Lock()
	supervisor.uploading[run.offer.RunID]++
	supervisor.mu.Unlock()
	go uploader.loop()
	return uploader
}

// add 分配序号并排队；从不阻塞在网络上。
func (u *eventUploader) add(event runtime.Event) {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.sequence++
	if redact := u.run.redact; redact != nil {
		event.Text, event.Payload = redact.text(event.Text), redact.json(event.Payload)
	}
	input := eventInput(u.run.offer.Generation, u.sequence, event)
	if len(u.pending) >= maxPendingEvents {
		u.spilled = true
		u.mu.Unlock()
		u.spill([]EventInput{input})
		return
	}
	u.pending = append(u.pending, input)
	u.pendingBytes += len(input.Payload)
	full := len(u.pending) >= eventBatchSize || u.pendingBytes >= eventBatchBytes
	u.mu.Unlock()
	if full {
		u.signal()
	}
}

func (u *eventUploader) signal() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// close 停止接收事件，上传剩余队列（失败则写入 outbox）后返回；可重复调用。
// 必须在上传结果之前调用：中心按当前最大序号 + 1 生成 run.result 事件，结果先到会占用设备尚未上传的序号。
func (u *eventUploader) close() {
	u.closeOnce.Do(func() {
		u.mu.Lock()
		u.closed = true
		u.mu.Unlock()
		u.signal()
		<-u.done
		supervisor := u.run.supervisor
		supervisor.mu.Lock()
		if supervisor.uploading[u.run.offer.RunID]--; supervisor.uploading[u.run.offer.RunID] <= 0 {
			delete(supervisor.uploading, u.run.offer.RunID)
		}
		supervisor.mu.Unlock()
	})
}

func (u *eventUploader) loop() {
	defer close(u.done)
	ticker := time.NewTicker(eventFlushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-u.wake:
		case <-ticker.C:
		}
		for {
			batch, closed := u.take()
			if len(batch) == 0 {
				if closed {
					u.drainOwn()
					return
				}
				break
			}
			u.upload(batch)
		}
	}
}

// take 取出一批（≤ eventBatchSize 条、≤ eventBatchBytes 字节，至少 1 条）。
func (u *eventUploader) take() ([]EventInput, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	count, size := 0, 0
	for count < len(u.pending) && count < eventBatchSize {
		next := len(u.pending[count].Payload)
		if count > 0 && size+next > eventBatchBytes {
			break
		}
		size += next
		count++
	}
	batch := append([]EventInput(nil), u.pending[:count]...)
	u.pending = u.pending[count:]
	u.pendingBytes -= size
	return batch, u.closed
}

func (u *eventUploader) upload(batch []EventInput) {
	err := u.run.supervisor.sendEvents(u.run.offer.OrganizationID, u.run.offer.RunID, batch, eventUploadAttempts)
	switch {
	case err == nil:
		u.mu.Lock()
		spilled := u.spilled
		u.mu.Unlock()
		if spilled {
			u.drainOwn()
		}
	case permanent(err):
		// 中心拒绝整批（与重试无关），丢弃并记录。
		u.run.logger.Warn("supervisor_events_rejected", "count", len(batch), "error", err.Error())
	default:
		u.run.logger.Warn("supervisor_events_upload_failed", "count", len(batch), "error", err.Error())
		u.mu.Lock()
		u.spilled = true
		u.mu.Unlock()
		u.spill(batch)
	}
}

func (u *eventUploader) spill(batch []EventInput) {
	journalContext, cancel := context.WithTimeout(context.Background(), defaultRequestTimeout)
	defer cancel()
	if err := u.run.supervisor.config.Journal.SaveEvents(journalContext, u.run.offer.OrganizationID, u.run.offer.RunID, batch); err != nil {
		u.run.logger.Warn("supervisor_events_spill_failed", "count", len(batch), "error", err.Error())
	}
}

// drainOwn 上传本 Run 在 outbox 中的事件（之前失败的批次）。
func (u *eventUploader) drainOwn() {
	u.mu.Lock()
	spilled := u.spilled
	u.mu.Unlock()
	if !spilled {
		return
	}
	drainContext, cancel := context.WithTimeout(context.Background(), outboxShutdownBudget)
	defer cancel()
	if u.run.supervisor.drainRun(drainContext, OutboxRun{OrganizationID: u.run.offer.OrganizationID, RunID: u.run.offer.RunID}) {
		u.mu.Lock()
		u.spilled = false
		u.mu.Unlock()
	}
}

// sendEvents 上传一批事件，瞬时失败按退避重试 attempts 次。
func (s *Supervisor) sendEvents(organizationID, runID string, batch []EventInput, attempts int) error {
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * eventRetryDelay)
		}
		if err = s.config.Client.AppendEvents(context.Background(), organizationID, runID, batch); err == nil || permanent(err) {
			return err
		}
	}
	return err
}

// drainRun 按序号分批上传某个 Run 在 outbox 中的事件；上传成功或被永久拒绝的批次删除。
// 返回 true 表示 outbox 中该 Run 已没有待上传事件。
func (s *Supervisor) drainRun(ctx context.Context, run OutboxRun) bool {
	journal := s.config.Journal
	for batch := 0; batch < outboxDrainBatches; batch++ {
		if ctx.Err() != nil {
			return false
		}
		events, err := journal.PendingEvents(ctx, run.RunID, eventBatchSize)
		if err != nil || len(events) == 0 {
			return err == nil
		}
		events = boundBatch(events)
		if err := s.sendEvents(run.OrganizationID, run.RunID, events, 1); err != nil && !permanent(err) {
			return false
		} else if err != nil {
			// 整批被拒（例如结果已先到，中心生成的 run.result 占用了其中一个序号而 409）：
			// 逐条重试，只丢弃确实被拒的事件。
			if !s.retryEach(run, events) {
				return false
			}
		}
		ids := make([]string, len(events))
		for index, event := range events {
			ids[index] = event.ID
		}
		if err := journal.DeleteEvents(ctx, ids); err != nil {
			return false
		}
	}
	return false
}

// retryEach 逐条上传；遇到瞬时失败返回 false（保留在 outbox），被永久拒绝的事件记录日志后放弃。
func (s *Supervisor) retryEach(run OutboxRun, events []EventInput) bool {
	rejected := 0
	for _, event := range events {
		err := s.sendEvents(run.OrganizationID, run.RunID, []EventInput{event}, 1)
		if err != nil && !permanent(err) {
			return false
		}
		if err != nil {
			rejected++
		}
	}
	if rejected > 0 {
		s.config.Logger.Warn("supervisor_outbox_events_rejected", "run_id", run.RunID, "count", rejected)
	}
	return true
}

// drainOutbox 尽力上传所有 Run 的 outbox 事件（轮询周期与 Daemon 退出时调用）。
func (s *Supervisor) drainOutbox(ctx context.Context) {
	runs, err := s.config.Journal.OutboxRuns(ctx)
	if err != nil {
		s.config.Logger.Warn("supervisor_outbox_list_failed", "error", err.Error())
		return
	}
	for _, run := range runs {
		s.mu.Lock()
		active := s.uploading[run.RunID] > 0
		s.mu.Unlock()
		if active {
			// 进行中的 Run 由自己的上传器负责，避免并发重复上传。
			continue
		}
		if !s.drainRun(ctx, run) && ctx.Err() != nil {
			return
		}
	}
}

func boundBatch(events []EventInput) []EventInput {
	size := 0
	for index, event := range events {
		size += len(event.Payload)
		if index > 0 && size > eventBatchBytes {
			return events[:index]
		}
	}
	return events
}

func eventInput(generation, sequence int64, event runtime.Event) EventInput {
	body := map[string]any{}
	if event.Text != "" {
		body["text"] = boundUTF8(event.Text, maxEventText)
	}
	if len(event.Payload) > 0 && len(event.Payload) <= maxEventData && json.Valid(event.Payload) {
		body["data"] = event.Payload
	}
	payload, _ := json.Marshal(body)
	if len(payload) > maxEventPayload {
		// 文本转义膨胀导致超出中心单条上限时，去掉文本保留结构化数据。
		delete(body, "text")
		payload, _ = json.Marshal(body)
	}
	occurredAt := event.At
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	return EventInput{
		ID: newUUID(), Generation: generation, Sequence: sequence,
		Type: string(event.Type), Payload: payload, OccurredAt: occurredAt.UTC(),
	}
}
