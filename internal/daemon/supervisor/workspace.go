package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"openfox/internal/daemon/workdir"
)

type savedWorkspace struct {
	Directory string
	Selection string
	Binding   workdir.Binding
}

func (j *Journal) readConversationWorkspace(ctx context.Context, device string, rc RunContext, conversation, agent string, source bool) (savedWorkspace, error) {
	var result savedWorkspace
	rows, err := j.db.QueryContext(ctx, `SELECT logical_directory,selection,binding FROM conversation_workdirs
		WHERE device_id=? AND organization_id=? AND space_id=? AND conversation_id=?
		  AND (?='' OR agent_id=?) AND (?=false OR task_id=?) LIMIT 2`, device, rc.OrganizationID, rc.SpaceID, conversation, agent, agent, source, rc.TaskID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return result, err
		}
		return result, sql.ErrNoRows
	}
	var payload []byte
	if err := rows.Scan(&result.Directory, &result.Selection, &payload); err != nil {
		return result, err
	}
	if rows.Next() {
		return result, workdir.ErrBindingConflict
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	return result, json.Unmarshal(payload, &result.Binding)
}

func (j *Journal) saveConversationWorkspace(ctx context.Context, device string, rc RunContext, saved *savedWorkspace) error {
	args := []any{device, rc.OrganizationID, rc.SpaceID, rc.ConversationID, rc.AgentID}
	if saved == nil {
		_, err := j.db.ExecContext(ctx, `DELETE FROM conversation_workdirs WHERE device_id=? AND organization_id=? AND space_id=? AND conversation_id=? AND agent_id=?`, args...)
		return err
	}
	payload, err := json.Marshal(saved.Binding)
	if err != nil {
		return err
	}
	args = append(args, rc.TaskID, saved.Directory, saved.Selection, string(payload))
	_, err = j.db.ExecContext(ctx, `INSERT INTO conversation_workdirs(device_id,organization_id,space_id,conversation_id,agent_id,task_id,logical_directory,selection,binding)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(device_id,organization_id,space_id,conversation_id,agent_id) DO UPDATE SET
		task_id=excluded.task_id,logical_directory=excluded.logical_directory,selection=excluded.selection,binding=excluded.binding`, args...)
	return err
}

func (r *runState) prepareConversationWorkspace(ctx context.Context, rc RunContext) (*workdir.PreparedWorkspace, error) {
	cfg := r.supervisor.config
	sel := rc.WorkspaceSelection
	if cfg.DeviceID == "" {
		if sel != nil {
			return nil, workdir.ErrBindingUnavailable
		}
		return r.prepareDefaultWorkspace(ctx, rc)
	}
	key := workdir.BindingKey{OrganizationID: rc.OrganizationID, SpaceID: rc.SpaceID, ConversationID: rc.ConversationID, AgentID: rc.AgentID}
	if !validWorkdirScope(key) || (sel != nil && sel.ComputerID != cfg.DeviceID) {
		return nil, workdir.ErrInvalidRequest
	}
	var prepared *workdir.PreparedWorkspace
	var saved *savedWorkspace
	var err error
	if sel != nil {
		prepared, saved, err = r.prepareSelection(ctx, rc, key)
	} else {
		prepared, saved, err = r.prepareRememberedDefault(ctx, rc, key)
	}
	if err != nil {
		return nil, err
	}
	if err = cfg.Journal.saveConversationWorkspace(ctx, cfg.DeviceID, rc, saved); err != nil {
		_ = prepared.Release()
		return nil, err
	}
	return prepared, nil
}

func (r *runState) prepareRememberedDefault(ctx context.Context, rc RunContext, key workdir.BindingKey) (*workdir.PreparedWorkspace, *savedWorkspace, error) {
	cfg := r.supervisor.config
	var directory workdir.RegisteredDirectory
	if rc.DirectoryMode == "EXPLICIT" && len(rc.DirectoryRoots) == 1 {
		var err error
		directory, err = workdir.ResolveDirectory(rc.DirectoryRoots[0], cfg.DirectoryRoots, rc.DirectoryRoots)
		if err != nil {
			return nil, nil, workdir.ErrRepositoryUnavailable
		}
		if rc.WorkdirIsolation != IsolationDirect && !directory.IsGitRoot(ctx) {
			return nil, nil, workdir.ErrNotGitRepository
		}
	}
	if directory.Name != "" && rc.SharedTaskCrewID == "" {
		prior, err := cfg.Journal.readConversationWorkspace(ctx, cfg.DeviceID, rc, rc.ConversationID, rc.AgentID, false)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		if err == nil && prior.Selection == "" && prior.Directory == directory.Name &&
			prior.Binding.ConfiguredPath == directory.Path && prior.Binding.Owned && rc.WorkdirIsolation != IsolationDirect {
			prepared, err := r.reopenWorkspace(ctx, key, prior.Binding, directory)
			if err == nil {
				return prepared, &prior, nil
			}
			// Normal storage cleanup may have removed an inactive worktree.
			if !errors.Is(err, workdir.ErrBindingUnavailable) {
				return nil, nil, err
			}
		}
	}
	prepared, err := r.prepareDefaultWorkspace(ctx, rc)
	if err != nil {
		return nil, nil, err
	}
	if directory.Name != "" && prepared.Binding.ConfiguredPath == directory.Path &&
		(!prepared.Owned || (prepared.RepositoryRoot == directory.Path && directory.IsGitRoot(ctx))) {
		return prepared, &savedWorkspace{Directory: directory.Name, Binding: prepared.Binding}, nil
	}
	return prepared, nil, nil
}

func validWorkdirScope(key workdir.BindingKey) bool {
	return key.OrganizationID != "" && key.SpaceID != "" && key.ConversationID != "" && key.AgentID != ""
}

func (r *runState) prepareSelection(ctx context.Context, rc RunContext, key workdir.BindingKey) (*workdir.PreparedWorkspace, *savedWorkspace, error) {
	cfg := r.supervisor.config
	sel := rc.WorkspaceSelection
	selectionBytes, err := json.Marshal(sel)
	if err != nil {
		return nil, nil, err
	}
	selection := string(selectionBytes)
	directoryName := sel.Directory
	var source savedWorkspace
	switch sel.Mode {
	case "DIRECT", "WORKTREE":
		if directoryName == "" || sel.SourceConversationID != "" || sel.SourceDirectory != "" || sel.SourceAgentID != "" || (sel.Mode == "DIRECT" && sel.BaseRef != "") {
			return nil, nil, workdir.ErrInvalidRequest
		}
	case "REUSE", "BASELINE":
		if rc.TaskID == "" || sel.SourceConversationID == "" || sel.SourceConversationID == rc.ConversationID || directoryName != "" || (sel.Mode == "REUSE" && sel.BaseRef != "") {
			return nil, nil, workdir.ErrInvalidRequest
		}
		source, err = cfg.Journal.readConversationWorkspace(ctx, cfg.DeviceID, rc, sel.SourceConversationID, sel.SourceAgentID, true)
		if err != nil || (sel.SourceDirectory != "" && sel.SourceDirectory != source.Directory) {
			return nil, nil, workdir.ErrBindingUnavailable
		}
		directoryName = source.Directory
	default:
		return nil, nil, workdir.ErrInvalidRequest
	}
	directory, err := workdir.ResolveDirectory(directoryName, cfg.DirectoryRoots, rc.DirectoryRoots)
	if err != nil {
		return nil, nil, err
	}
	existing, err := cfg.Journal.readConversationWorkspace(ctx, cfg.DeviceID, rc, rc.ConversationID, rc.AgentID, false)
	var prepared *workdir.PreparedWorkspace
	switch {
	case err == nil:
		if existing.Selection != selection || directory.Name != existing.Directory || (sel.Mode == "REUSE" && existing.Binding.ActualPath != source.Binding.ActualPath) {
			return nil, nil, workdir.ErrBindingConflict
		}
		prepared, err = r.reopenWorkspace(ctx, key, existing.Binding, directory)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, nil, err
	case sel.Mode == "DIRECT":
		prepared, err = r.prepareDirect(ctx, key, directory.Path)
	case sel.Mode == "REUSE":
		prepared, err = r.reopenWorkspace(ctx, key, source.Binding, directory)
	default:
		if !directory.IsGitRoot(ctx) {
			return nil, nil, workdir.ErrNotGitRepository
		}
		// The empty BASELINE branch means the source checkout's own HEAD.
		// Hold its lease while resolving the commit and creating the new worktree.
		var checkout string
		if sel.Mode == "BASELINE" {
			original, e := r.reopenWorkspace(ctx, key, source.Binding, directory)
			if e != nil {
				return nil, nil, e
			}
			defer original.Release()
			checkout = original.Path
		}
		prepared, err = cfg.Workdir.Prepare(ctx, workdir.Request{Key: key, Mode: workdir.ModeSpecifiedGit, SourcePath: directory.Path,
			BaseRef: sel.BaseRef, LocalBaseOnly: true, LocalHeadPath: checkout})
	}
	if err != nil {
		return nil, nil, err
	}
	return prepared, &savedWorkspace{Directory: directoryName, Selection: selection, Binding: prepared.Binding}, nil
}

func (r *runState) reopenWorkspace(ctx context.Context, key workdir.BindingKey, b workdir.Binding, d workdir.RegisteredDirectory) (*workdir.PreparedWorkspace, error) {
	deadline := time.Now().Add(directWaitLimit)
	for {
		prepared, err := r.supervisor.config.Workdir.ReopenSelection(ctx, key, b, d)
		if !workdir.IsPathConflict(err) {
			return prepared, err
		}
		if time.Now().After(deadline) {
			return nil, errDirectBusy
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(directWaitPoll):
		}
	}
}
