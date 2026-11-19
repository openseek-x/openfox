package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/execution"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/runtimepolicy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

const TaskDraftCommand = "task_draft"

type taskDraft struct {
	id, space, agent string
	actor            conversations.Actor
	input            conversations.TaskCreationRequest
}

func (d taskDraft) resolve(ctx context.Context, tx pgx.Tx, org string) (string, runParams, error) {
	agent, err := (conversations.Service{}).ResolveDraftAgent(ctx, tx, org, d.actor, conversations.CreateInput{
		ID: d.id, SpaceID: d.space, AgentID: d.input.AgentID, CrewID: d.input.CrewID, Visibility: conversations.VisibilityPrivate,
	})
	if err != nil {
		return "", runParams{}, err
	}
	reason, p, err := agentRunParams(ctx, tx, agent, d.actor.UserID, d.space)
	if err != nil {
		return "", p, err
	}
	if reason != "" || (d.agent != "" && d.agent != agent) {
		return "", p, conversations.ErrForbidden
	}
	ok, err := variantRunAuthorized(ctx, tx, dispatchRun{Run: execution.Run{OrganizationID: org, AgentID: agent, ActorID: d.actor.UserID, ComputerID: p.ComputerID}})
	if err != nil {
		return "", p, err
	}
	if !ok {
		return "", p, conversations.ErrForbidden
	}
	return agent, p, nil
}

func EnqueueTaskDraft(ctx context.Context, tx pgx.Tx, org, space, id string, actor conversations.Actor, input conversations.TaskCreationRequest) (string, error) {
	d := taskDraft{id: id, space: space, actor: actor, input: input}
	agent, p, err := d.resolve(ctx, tx, org)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.task_drafts(organization_id,id,space_id,requester_id,agent_id,brief,options)
	VALUES(openfox_private.current_organization_id(),$1,$2,$3,$4,$5,$6)`, id, space, actor.UserID, agent, input.Brief, input.TaskCreationOptions)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.device_commands(organization_id,id,device_id,kind,requested_by_user_id,expires_at)
	VALUES(openfox_private.current_organization_id(),$1,$2,'task_draft',$3,now()+make_interval(secs=>$4))`, id, p.ComputerID, actor.UserID, p.QueueTimeoutSeconds)
	return p.ComputerID, err
}

func readTaskDraft(ctx context.Context, tx pgx.Tx, id string) (taskDraft, error) {
	d := taskDraft{id: id}
	var role string
	err := tx.QueryRow(ctx, `SELECT d.space_id::text,d.agent_id::text,d.requester_id::text,d.brief,d.options,lower(m.role),m.authorization_version
	FROM openfox.task_drafts d JOIN openfox.organization_members m ON m.organization_id=d.organization_id AND m.user_id=d.requester_id
	WHERE d.organization_id=openfox_private.current_organization_id() AND d.id=$1 AND d.finished_at IS NULL FOR UPDATE OF d`, id).
		Scan(&d.space, &d.agent, &d.actor.UserID, &d.input.Brief, &d.input.TaskCreationOptions, &role, &d.actor.AuthorizationVersion)
	d.actor.Role = policy.OrganizationRole(role)
	return d, err
}

// TaskDraftContext is only returned in the authenticated device's successful claim response.
func TaskDraftContext(ctx context.Context, tx pgx.Tx, box *secrets.Box, org, device, id string) (RunContext, error) {
	d, err := readTaskDraft(ctx, tx, id)
	if err != nil {
		return RunContext{}, err
	}
	_, p, err := d.resolve(ctx, tx, org)
	if err != nil {
		return RunContext{}, err
	}
	if p.ComputerID != device {
		return RunContext{}, conversations.ErrForbidden
	}
	cfg := runtimepolicy.Effective(p.Effective, runtimepolicy.Overrides{})
	rc := RunContext{Runtime: cfg.Runtime, Model: cfg.Model, ApprovalMode: cfg.ApprovalMode, ReasoningEffort: cfg.ReasoningEffort, Prompt: d.input.Brief}
	err = tx.QueryRow(ctx, `SELECT concat_ws(E'\n\n',NULLIF(c.instructions,''),NULLIF(cr.instructions,'')), c.custom_args
	FROM openfox.agent_config_versions c LEFT JOIN openfox.agent_crews cr ON cr.organization_id=c.organization_id AND cr.id=NULLIF($3,'')::uuid
	WHERE c.organization_id=openfox_private.current_organization_id() AND c.agent_id=$1 AND c.config_version=$2`, d.agent, p.ConfigVersion, d.input.CrewID).Scan(&rc.Instructions, &rc.CustomArgs)
	if err != nil {
		return RunContext{}, err
	}
	rc.Environment, err = loadAgentEnvironment(ctx, tx, box, org, d.agent, device, cfg.Runtime)
	return rc, err
}

func draftRejected(err error) bool {
	var rule *collaboration.RuleError
	if errors.As(err, &rule) || collaboration.IsPriorityLevelViolation(err) {
		return true
	}
	for _, cause := range []error{
		conversations.ErrForbidden, conversations.ErrNotFound, conversations.ErrInvalidRequest, membership.ErrAuthorizationStale,
		collaboration.ErrInvalidTask, collaboration.ErrSpaceNotFound, collaboration.ErrSpaceRetired, collaboration.ErrSpaceMemberNotFound,
		collaboration.ErrInvalidPriority, collaboration.ErrInvalidStatus, collaboration.ErrInvalidWorkflow, collaboration.ErrTaskNotFound,
		collaboration.ErrTaskTypeNotFound, collaboration.ErrStatusNotFound, collaboration.ErrStatusRequired, collaboration.ErrActionForbidden,
		collaboration.ErrOrganizationForbidden,
	} {
		if errors.Is(err, cause) {
			return true
		}
	}
	return false
}

func (w *Worker) finishTaskDrafts(ctx context.Context, org string, limit int) error {
	type pending struct {
		id, user string
		version  uint64
	}
	var work []pending
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT d.id::text,d.requester_id::text,m.authorization_version FROM openfox.task_drafts d
		JOIN openfox.organization_members m ON m.organization_id=d.organization_id AND m.user_id=d.requester_id
		LEFT JOIN openfox.device_commands c ON c.organization_id=d.organization_id AND c.id=d.id
		WHERE d.organization_id=openfox_private.current_organization_id() AND d.finished_at IS NULL
		  AND (c.id IS NULL OR c.status IN ('SUCCEEDED','FAILED','EXPIRED') OR c.expires_at<=now())
		ORDER BY d.id LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x pending
			if err := rows.Scan(&x.id, &x.user, &x.version); err != nil {
				return err
			}
			work = append(work, x)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	var first error
	for _, x := range work {
		sel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: x.user, AuthorizationVersion: x.version}
		err := w.Pool.RunForOrganization(ctx, org, tenancy.SourceOrganizationWorker, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil }), func(ctx context.Context, tx pgx.Tx) error { return finishTaskDraft(ctx, tx, org, x.id) })
		if err != nil {
			w.logger().Warn("task_draft_completion_failed", "organization_id", org, "request_id", x.id, "error", err.Error())
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func finishTaskDraft(ctx context.Context, tx pgx.Tx, org, id string) error {
	// Lock in claim order and recheck the deadline. Another worker may already have applied the result.
	var status, device string
	var result json.RawMessage
	var expired bool
	err := tx.QueryRow(ctx, `SELECT status,device_id::text,result,expires_at<=now() FROM openfox.device_commands WHERE organization_id=openfox_private.current_organization_id() AND id=$1 FOR UPDATE`, id).Scan(&status, &device, &result, &expired)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && (status == "DELIVERED" || status == "PENDING") && !expired {
		return nil
	}
	d, err := readTaskDraft(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var taskID string
	if status == "SUCCEEDED" {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		taskID, err = d.create(ctx, sp, org, device, result)
		if err != nil {
			if e := sp.Rollback(ctx); e != nil {
				return e
			}
			if !draftRejected(err) {
				return err
			}
		} else if err := sp.Commit(ctx); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.task_drafts SET finished_at=now(),task_id=NULLIF($2,'')::uuid WHERE organization_id=openfox_private.current_organization_id() AND id=$1`, id, taskID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT openfox_private.notify_task_draft($1)`, id)
	return err
}

func (d taskDraft) create(ctx context.Context, tx pgx.Tx, org, device string, result json.RawMessage) (string, error) {
	_, p, err := d.resolve(ctx, tx, org)
	if err != nil {
		return "", err
	}
	if p.ComputerID != device {
		return "", conversations.ErrForbidden
	}
	var draft struct {
		Title, Description string
		Email              string `json:"assignee_email"`
		Name               string `json:"assignee_name"`
	}
	if json.Unmarshal(result, &draft) != nil {
		return "", collaboration.ErrInvalidTask
	}
	title, description := strings.TrimSpace(draft.Title), strings.TrimSpace(draft.Description)
	if title == "" || utf8.RuneCountInString(title) > 200 || description == "" || utf8.RuneCountInString(description) > 10000 {
		return "", collaboration.ErrInvalidTask
	}
	o := d.input.TaskCreationOptions
	in := collaboration.TaskInput{ID: d.id, RequestID: "task-draft:" + d.id, Title: title, Description: description, Priority: o.Priority, DueDate: o.DueDate, ProjectID: o.ProjectID, TypeID: o.TypeID, StatusID: o.StatusID, IterationID: o.IterationID, ParentTaskID: o.ParentTaskID, SubmitToTriage: o.SubmitToTriage}
	name, email := strings.TrimSpace(draft.Name), strings.TrimSpace(draft.Email)
	if email != "" || name != "" {
		value := email
		if value == "" {
			value = name
		}
		if !strings.Contains(strings.ToLower(d.input.Brief), strings.ToLower(value)) {
			return "", collaboration.ErrInvalidTask
		}
		rows, err := tx.Query(ctx, `SELECT u.id::text FROM openfox.organization_members m JOIN openfox.users u ON u.id=m.user_id
			WHERE m.organization_id=openfox_private.current_organization_id() AND m.status='ACTIVE' AND lower(CASE WHEN $1 THEN u.email ELSE u.full_name END)=lower($2) LIMIT 2`, email != "", value)
		if err != nil {
			return "", err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return "", err
		}
		if len(ids) != 1 {
			return "", collaboration.ErrInvalidTask
		}
		in.AssigneeUserID = ids[0]
	} else if o.CrewID != "" {
		in.Assignees = &[]collaboration.TaskAssigneeRef{{Kind: "CREW", ID: o.CrewID}}
	} else {
		in.ExecutorAgentID = d.agent
	}
	task, err := (collaboration.Service{}).CreateTask(ctx, tx, org, d.space, collaboration.Actor(d.actor), in)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.task_activities(organization_id,space_id,task_id,id,actor_id,kind,safe_diff,source_id)
	VALUES(openfox_private.current_organization_id(),$1,$2,gen_random_uuid(),$3,'task.via_agent',jsonb_build_object('agent_id',$4::text),'task-draft:' || $2::uuid::text)`, d.space, task.ID, d.actor.UserID, d.agent)
	if err != nil {
		return "", err
	}
	return task.ID, nil
}
