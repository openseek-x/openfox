package integrations

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

// BuglyReceipt is the documented synchronous creation response. A nonzero code
// never contains a task URL. HTTPStatus is local transport metadata, not JSON.
type BuglyReceipt struct {
	Code       int    `json:"code"`
	Message    string `json:"message"`
	URL        string `json:"url"`
	HTTPStatus int    `json:"-"`
}

// ReceiveBugly commits a durable sanitized failure receipt independently of the
// creation savepoint. The caller must commit this outer transaction before 200.
// There is no remote I/O, user impersonation fallback, or asynchronous fake URL.
func (s Service) ReceiveBugly(ctx context.Context, tx pgx.Tx, lookup GitWebhookLookup, token string, body []byte) (BuglyReceipt, error) {
	c, _, hash, err := s.buglyConnection(ctx, tx, lookup.ConnectionID, true)
	if err != nil {
		return BuglyReceipt{}, err
	}
	if c.Status != StatusActive || !buglyTokenEqual(hash, token) {
		return BuglyReceipt{}, ErrBuglyInactive
	}
	receipt := BuglyReceipt{Code: 1, Message: "invalid_payload", HTTPStatus: 400}
	status, detail := "REJECTED", "invalid_payload"
	p, parseErr := ParseBuglyPayload(body)
	if parseErr == nil {
		nested, e := tx.Begin(ctx)
		if e != nil {
			return BuglyReceipt{}, e
		}
		var pauseID string
		receipt, status, detail, pauseID, err = s.createBuglyIssue(ctx, nested, lookup, p)
		if err != nil {
			if e = nested.Rollback(ctx); e != nil {
				return BuglyReceipt{}, e
			}
			if receipt.Message == "" {
				receipt = BuglyReceipt{Code: 1, Message: "creation_failed", HTTPStatus: 422}
				status, detail = "FAILED", "creation_failed"
				var dbErr *pgconn.PgError
				if errors.As(err, &dbErr) {
					receipt.HTTPStatus = 503
					receipt.Message = "creation_unavailable"
				}
			}
			if pauseID != "" {
				if _, e = tx.Exec(ctx, `UPDATE openfox.integration_bugly_bindings SET status='NEEDS_ATTENTION',status_reason=$2,version=version+1,updated_at=clock_timestamp()
      WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE'`, pauseID, detail); e != nil {
					return BuglyReceipt{}, e
				}
			}
		} else if e = nested.Commit(ctx); e != nil {
			return BuglyReceipt{}, e
		}
	}
	id, e := membership.NewID()
	if e != nil {
		return BuglyReceipt{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_webhook_deliveries(organization_id,connection_id,provider,delivery_id,event,action,signature_status,status,detail)
  VALUES(openfox_private.current_organization_id(),$1::uuid,'bugly',$2,'issue.create',$3,'SECRET_URL',$4,$5)`, c.ID, id, detail, status, detail)
	if e != nil {
		return BuglyReceipt{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET webhook_last_delivery_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.ID)
	return receipt, e
}

func (s Service) createBuglyIssue(ctx context.Context, tx pgx.Tx, lookup GitWebhookLookup, p BuglyPayload) (out BuglyReceipt, status, detail, pause string, err error) {
	fail := func(message string, httpStatus int, e error, pauseID string) (BuglyReceipt, string, string, string, error) {
		return BuglyReceipt{Code: 1, Message: message, HTTPStatus: httpStatus}, "REJECTED", message, pauseID, e
	}
	b, e := scanBuglyBinding(tx.QueryRow(ctx, `SELECT `+buglyBindingColumns+` FROM openfox.integration_bugly_bindings
  WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND product_id=$2 AND removed_at IS NULL FOR UPDATE`, lookup.ConnectionID, p.ProductID))
	if e != nil {
		if errors.Is(e, ErrNotFound) {
			return fail("product_not_bound", 422, e, "")
		}
		return out, "", "", "", e
	}
	if b.Status != "ACTIVE" {
		return fail("binding_inactive", 409, ErrBuglyInactive, "")
	}
	actor, e := buglySpaceActor(ctx, tx, lookup.OrganizationID, b.AuthorizedBy, b.SpaceID, true)
	if e != nil {
		if errors.Is(e, ErrForbidden) || errors.Is(e, collaboration.ErrSpaceRetired) || errors.Is(e, collaboration.ErrSpaceNotFound) {
			return fail("authorization_lost", 403, e, b.ID)
		}
		return out, "", "", "", e
	}
	if e = buglyType(ctx, tx, b.SpaceID, b.TaskTypeID); e != nil {
		if errors.Is(e, ErrInvalidRequest) {
			return fail("task_type_unavailable", 422, e, b.ID)
		}
		return out, "", "", "", e
	}
	var taskID *string
	e = tx.QueryRow(ctx, `SELECT task_id::text FROM openfox.integration_bugly_sources WHERE organization_id=openfox_private.current_organization_id()
  AND connection_id=$1::uuid AND binding_id=$2::uuid AND product_id=$3 AND issue_key=$4 FOR UPDATE`, b.ConnectionID, b.ID, p.ProductID, p.IssueKey).Scan(&taskID)
	duplicate := e == nil
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, "", "", "", e
	}
	if duplicate && taskID == nil {
		return fail("source_task_unavailable", 409, ErrBuglySourceDeleted, "")
	}
	if !duplicate {
		priority := "NONE"
		if v, ok := b.PriorityMapping[p.Priority]; ok {
			priority = v
		}
		task, e := (collaboration.Service{}).CreateInboundIssueTask(ctx, tx, lookup.OrganizationID, b.SpaceID, actor, collaboration.ImportTaskInput{
			Title: p.Title, Description: p.Description, TypeID: b.TaskTypeID, Priority: priority,
		})
		if e != nil {
			return out, "", "", "", e
		}
		taskID = &task.ID
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_bugly_sources(organization_id,connection_id,binding_id,product_id,issue_key,issue_type,task_id,task_space_id)
   VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6::uuid,$7::uuid)`, b.ConnectionID, b.ID, p.ProductID, p.IssueKey, p.IssueType, task.ID, task.SpaceID)
		if e != nil {
			return out, "", "", "", e
		}
	}
	var space, prefix string
	var sequence int64
	e = tx.QueryRow(ctx, `SELECT t.space_id::text,s.prefix,t.sequence FROM openfox.tasks t JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id
  WHERE t.organization_id=openfox_private.current_organization_id() AND t.id=$1::uuid AND t.deleted_at IS NULL AND s.deleted_at IS NULL FOR SHARE OF t,s`, *taskID).Scan(&space, &prefix, &sequence)
	if errors.Is(e, pgx.ErrNoRows) {
		return fail("source_task_unavailable", 409, ErrBuglySourceDeleted, "")
	}
	if e != nil {
		return out, "", "", "", e
	}
	access, e := (collaboration.Service{}).LoadSpaceAccess(ctx, tx, lookup.OrganizationID, space, actor.UserID)
	if e == nil {
		e = buglyAuthorizeAccess(access, lookup.OrganizationID, actor.UserID, actor.Role, policy.ActionReadSpace)
	}
	if e != nil {
		if errors.Is(e, ErrForbidden) || errors.Is(e, collaboration.ErrSpaceNotFound) {
			return fail("task_access_lost", 403, e, "")
		}
		return out, "", "", "", e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_bugly_sources SET task_space_id=$2::uuid WHERE organization_id=openfox_private.current_organization_id() AND task_id=$1::uuid`, *taskID, space)
	if e != nil {
		return out, "", "", "", e
	}
	base, e := s.buglyPublicURL()
	if e != nil {
		return out, "", "", "", e
	}
	taskURL := fmt.Sprintf("%s/o/%s/spaces/%s/tasks?task=%s", base, lookup.OrganizationID, space, url.QueryEscape(fmt.Sprintf("%s-%d", prefix, sequence)))
	detail, status = "created", "PROCESSED"
	if duplicate {
		detail, status = "duplicate", "IGNORED"
	}
	return BuglyReceipt{Code: 0, Message: "success", URL: taskURL, HTTPStatus: 200}, status, detail, "", nil
}
