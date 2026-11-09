package supervisor

import (
	"time"

	"openfox/internal/runtime"
)

// prep_env 步骤（固定 id "prep"）：claim 后开始，子步骤按 workdir → repo（仅 EXPLICIT）→ runtime → session
// 依次推进；Runtime 启动成功后 done，prepare-failed 时当前子步骤与整体 failed，取消时 interrupted。
const (
	prepStepID   = "prep"
	prepWorkdir  = "workdir"
	prepRepo     = "repo"
	prepRuntime  = "runtime"
	prepSession  = "session"
	prepSubsteps = 4
)

type prepProgress struct {
	startedAt time.Time
	substeps  []runtime.StepSubstep
	ended     bool
}

// prepBegin 发出 running 的 prep_env，第一个子步骤 workdir 运行中。
func (r *runState) prepBegin() {
	r.prep = prepProgress{startedAt: time.Now().UTC(), substeps: []runtime.StepSubstep{{Kind: prepWorkdir, State: runtime.StepRunning}}}
	r.emitPrep(runtime.StepRunning)
}

// prepSubstep 结束当前运行中的子步骤并开始 kind。
func (r *runState) prepSubstep(kind, label string) {
	if r.prep.ended || r.prep.startedAt.IsZero() {
		return
	}
	r.finishRunning(runtime.StepDone)
	r.prep.substeps = append(r.prep.substeps, runtime.StepSubstep{Kind: kind, State: runtime.StepRunning, Label: boundUTF8(label, 256)})
	r.emitPrep(runtime.StepRunning)
}

// prepEnd 以终态结束 prep_env；只生效一次。done 时补齐 session 子步骤。
func (r *runState) prepEnd(state string) {
	if r.prep.ended || r.prep.startedAt.IsZero() {
		return
	}
	r.prep.ended = true
	r.finishRunning(state)
	if state == runtime.StepDone {
		r.prep.substeps = append(r.prep.substeps, runtime.StepSubstep{Kind: prepSession, State: runtime.StepDone})
	}
	r.emitPrep(state)
}

func (r *runState) finishRunning(state string) {
	for index := range r.prep.substeps {
		if r.prep.substeps[index].State == runtime.StepRunning {
			r.prep.substeps[index].State = state
		}
	}
}

func (r *runState) emitPrep(state string) {
	if r.events == nil {
		return
	}
	now := time.Now().UTC()
	started := r.prep.startedAt
	done := 0
	for _, substep := range r.prep.substeps {
		if substep.State == runtime.StepDone {
			done++
		}
	}
	progress := float64(done) / prepSubsteps
	if state == runtime.StepDone || progress > 1 {
		progress = 1
	}
	step := runtime.Step{
		ID: prepStepID, Kind: runtime.StepKindPrepEnv, State: state, StartedAt: &started,
		Substeps: append([]runtime.StepSubstep(nil), r.prep.substeps...), Progress: &progress,
	}
	if state != runtime.StepRunning {
		duration := now.Sub(started).Milliseconds()
		step.EndedAt, step.DurationMS = &now, &duration
	}
	payload, err := runtime.StepPayload(step)
	if err != nil {
		return
	}
	r.events.add(runtime.Event{Type: runtime.EventStep, At: now, Payload: payload})
}
