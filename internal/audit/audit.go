// Package audit defines the application audit event and writer boundary.
// Events contain identifiers and allowlisted metadata only; persistence is
// supplied by another layer so this package stays independent of databases.
package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidEvent = errors.New("invalid audit event")
	ErrNilWriter    = errors.New("nil audit writer")
)

type ResourceRef struct {
	Kind           string
	OrganizationID string
	SpaceID        string
	ID             string
}

// Event is deliberately narrower than a general log payload. Do not add
// request bodies, message content, credentials, tokens, headers, or secrets.
type Event struct {
	ID                   string
	OrganizationID       string
	ActorID              string
	Action               string
	Decision             string
	Reason               string
	Resource             ResourceRef
	RequestID            string
	AuthorizationVersion uint64
	SafeMetadata         map[string]string
	CreatedAt            time.Time
}

type Writer interface {
	WriteAudit(context.Context, Event) error
}

// Record validates before invoking the persistence port. A database writer
// may wrap the call in the caller's business transaction or an outbox.
func Record(ctx context.Context, writer Writer, event Event) error {
	if writer == nil {
		return ErrNilWriter
	}
	if err := event.Validate(); err != nil {
		return err
	}
	return writer.WriteAudit(ctx, event)
}

func (event Event) Validate() error {
	if event.ID == "" {
		return invalidField("id")
	}
	if event.OrganizationID == "" {
		return invalidField("organization_id")
	}
	if event.ActorID == "" {
		return invalidField("actor_id")
	}
	if !stableToken(event.Action) {
		return invalidField("action")
	}
	if !stableToken(event.Decision) {
		return invalidField("decision")
	}
	if event.Reason != "" && !stableToken(event.Reason) {
		return invalidField("reason")
	}
	if event.RequestID == "" {
		return invalidField("request_id")
	}
	if event.CreatedAt.IsZero() || event.CreatedAt.Location() == nil {
		return invalidField("created_at")
	}
	if err := event.Resource.validate(event.OrganizationID); err != nil {
		return err
	}
	if len(event.SafeMetadata) > 16 {
		return invalidField("safe_metadata")
	}
	for key, value := range event.SafeMetadata {
		if !allowlistedMetadataKey(key) || strings.TrimSpace(value) == "" || len(value) > 256 || strings.IndexFunc(value, isControl) >= 0 {
			return invalidField("safe_metadata")
		}
	}
	return nil
}

func (resource ResourceRef) validate(organizationID string) error {
	if !stableToken(resource.Kind) {
		return invalidField("resource.kind")
	}
	if resource.OrganizationID == "" || resource.OrganizationID != organizationID {
		return invalidField("resource.organization_id")
	}
	if resource.ID == "" {
		return invalidField("resource.id")
	}
	if resource.SpaceID != "" && !stableToken(resource.SpaceID) {
		return invalidField("resource.space_id")
	}
	return nil
}

func allowlistedMetadataKey(key string) bool {
	switch key {
	case "client", "source", "result", "status", "version", "grant_scope", "resource_kind", "space_id",
		// 替换电脑审计（设备 ID 与计数，不含敏感信息）。
		"replaced_device_id", "moved_agents", "canceled_runs",
		// 任务移动审计 task.moved（空间 ID、编号与计数，不含任务内容）。
		"source_space_id", "target_space_id", "previous_display_id", "display_id", "moved_tasks":
		return true
	default:
		return false
	}
}

func stableToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '_' && character != '-' && character != '.' {
			return false
		}
	}
	return true
}

func isControl(character rune) bool {
	return character < 0x20 || character == 0x7f
}

func invalidField(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidEvent, field)
}
