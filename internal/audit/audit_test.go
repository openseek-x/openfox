package audit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRecordValidatesAndWritesSafeEvent(t *testing.T) {
	writer := &recordingWriter{}
	event := testEvent()

	if err := Record(context.Background(), writer, event); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if writer.calls != 1 {
		t.Fatalf("writer calls = %d, want 1", writer.calls)
	}
	if writer.event.ID != event.ID || writer.event.SafeMetadata["source"] != "web" {
		t.Fatalf("writer event = %#v, want original safe event", writer.event)
	}
}

func TestRecordRejectsSecretBearingMetadataBeforeWriting(t *testing.T) {
	writer := &recordingWriter{}
	event := testEvent()
	event.SafeMetadata = map[string]string{"token": "must-not-be-stored"}

	err := Record(context.Background(), writer, event)
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Record() error = %v, want ErrInvalidEvent", err)
	}
	if writer.calls != 0 {
		t.Fatalf("writer calls = %d, want 0", writer.calls)
	}
}

func TestEventValidationRejectsCrossOrganizationResource(t *testing.T) {
	event := testEvent()
	event.Resource.OrganizationID = "org-b"

	if err := event.Validate(); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Validate() error = %v, want ErrInvalidEvent", err)
	}
}

func TestRecordPropagatesWriterError(t *testing.T) {
	wantErr := errors.New("storage unavailable")
	writer := &recordingWriter{err: wantErr}

	if err := Record(context.Background(), writer, testEvent()); !errors.Is(err, wantErr) {
		t.Fatalf("Record() error = %v, want writer error", err)
	}
}

func TestEventValidationRequiresStableFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{name: "missing action", mutate: func(event *Event) { event.Action = "" }},
		{name: "unsafe action", mutate: func(event *Event) { event.Action = "read secret" }},
		{name: "missing resource", mutate: func(event *Event) { event.Resource.ID = "" }},
		{name: "invalid metadata value", mutate: func(event *Event) { event.SafeMetadata["source"] = "line\nbreak" }},
		{name: "zero timestamp", mutate: func(event *Event) { event.CreatedAt = time.Time{} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := testEvent()
			test.mutate(&event)
			if err := event.Validate(); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Validate() error = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestRecordRejectsNilWriter(t *testing.T) {
	if err := Record(context.Background(), nil, testEvent()); !errors.Is(err, ErrNilWriter) {
		t.Fatalf("Record() error = %v, want ErrNilWriter", err)
	}
}

type recordingWriter struct {
	calls int
	event Event
	err   error
}

func (writer *recordingWriter) WriteAudit(_ context.Context, event Event) error {
	writer.calls++
	writer.event = event
	return writer.err
}

func testEvent() Event {
	return Event{
		ID:             "audit-1",
		OrganizationID: "org-a",
		ActorID:        "user-a",
		Action:         "object.read",
		Decision:       "allow",
		Reason:         "allowed",
		Resource: ResourceRef{
			Kind:           "task",
			OrganizationID: "org-a",
			SpaceID:        "space-a",
			ID:             "task-1",
		},
		RequestID:            "request-1",
		AuthorizationVersion: 3,
		SafeMetadata:         map[string]string{"source": "web"},
		CreatedAt:            time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	}
}
