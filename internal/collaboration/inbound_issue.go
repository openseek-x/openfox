package collaboration

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// CreateInboundIssueTask is a trusted live integration entry. Only these four
// fields cross into the task writer; external identities and arbitrary import
// fields are never task assignment or execution authority. Existing triage
// rules run normally and the returned task carries its final space.
func (s Service) CreateInboundIssueTask(ctx context.Context, tx pgx.Tx, orgID, spaceID string, actor Actor, input ImportTaskInput) (Task, error) {
	return s.CreateTask(ctx, tx, orgID, spaceID, actor, TaskInput{
		Title: input.Title, Description: input.Description, TypeID: input.TypeID,
		Priority: input.Priority, integrationSource: true,
	})
}
