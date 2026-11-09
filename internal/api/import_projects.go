package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/integrations"
)

func (h *Handler) listImportProjects(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var source integrations.ProjectSource
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		source, err = h.integrations.ProjectSource(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("provider"), r.URL.Query().Get("connection_id"))
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	projects, err := source.Projects(ctx)
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, projects)
}

func (h *Handler) createProjectImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Credentials  *integrations.ProviderTokenInput `json:"credentials"`
		ConnectionID string                           `json:"connection_id"`
		ProjectID    string                           `json:"project_id"`
		SpaceID      string                           `json:"space_id"`
		NewSpace     *imports.NewSpace                `json:"new_space"`
	}
	if err := decodeJSON(w, r, &input); err != nil || (input.NewSpace == nil) == (input.SpaceID == "") || input.ProjectID == "" || (input.Credentials == nil) == (input.ConnectionID == "") || input.Credentials != nil && r.PathValue("provider") != "tapd" {
		writeImportError(w, r, imports.ErrInvalidRequest)
		return
	}
	if input.NewSpace != nil {
		ns, err := imports.NormalizeNewSpace(*input.NewSpace, "")
		if err != nil {
			writeImportError(w, r, err)
			return
		}
		input.NewSpace = &ns
	}
	target := func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		if input.NewSpace != nil {
			return checkNewSpacePrefix(ctx, tx, s, call, input.NewSpace.Prefix)
		}
		return authorizeImportSpace(ctx, tx, s, call, input.SpaceID)
	}
	provider := r.PathValue("provider")
	sourceFor := func(ctx context.Context, tx pgx.Tx) (integrations.ProjectSource, error) {
		if input.Credentials != nil {
			return h.integrations.TAPDImportSource(ctx, tx, call.organizationID, call.selected.Actor, *input.Credentials)
		}
		return h.integrations.ProjectSource(ctx, tx, call.organizationID, call.selected.Actor, provider, input.ConnectionID)
	}
	var source integrations.ProjectSource
	var sourceErr error
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		source, sourceErr = sourceFor(ctx, tx)
		if sourceErr != nil {
			return sourceErr
		}
		return target(ctx, tx, s)
	})
	if sourceErr != nil {
		writeIntegrationError(w, r, sourceErr)
		return
	}
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	// The token may see projects outside the bound company. Limit draft creation
	// to the same project list offered by this connection's picker.
	projects, err := source.Projects(ctx)
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	var name string
	for _, p := range projects {
		if p.ID == input.ProjectID {
			name = p.Name
			break
		}
	}
	if name == "" {
		writeIntegrationError(w, r, integrations.ErrNotFound)
		return
	}
	rows, err := source.ProjectRows(ctx, input.ProjectID)
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	parsed, err := parseProjectRows(provider+"_api", rows)
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	var identity, displayName string
	if input.Credentials != nil {
		identity, displayName, err = integrations.VerifyProviderToken(ctx, "tapd", *input.Credentials)
		if err != nil {
			writeIntegrationError(w, r, err)
			return
		}
	}
	account := source.ExternalID
	if input.Credentials != nil {
		account = identity
	}
	parsed.Origin = imports.ProjectOrigin{Project: input.ProjectID, Connection: input.ConnectionID, Instance: fmt.Sprintf("%x", sha256.Sum256([]byte(account+"\x00"+input.ProjectID)))}
	var job imports.Job
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, s collaboration.Service) error {
		var latest integrations.ProjectSource
		latest, sourceErr = sourceFor(ctx, tx)
		if sourceErr == nil && latest.Version != source.Version {
			sourceErr = integrations.ErrVersionConflict
		}
		if sourceErr != nil {
			return sourceErr
		}
		if err := target(ctx, tx, s); err != nil {
			return err
		}
		var err error
		if input.NewSpace != nil {
			job, err = (imports.Service{}).UploadNewSpace(ctx, tx, call.organizationID, call.selected.Actor, *input.NewSpace, name, parsed)
		} else {
			job, err = (imports.Service{}).Upload(ctx, tx, call.organizationID, call.selected.Actor, input.SpaceID, name, parsed)
		}
		if err == nil && input.Credentials != nil {
			sourceErr = h.integrations.StageTAPDConnection(ctx, tx, call.organizationID, job.ID, call.selected.Actor.UserID, *input.Credentials, identity, displayName)
			return sourceErr
		}
		return err
	})
	if sourceErr != nil {
		writeIntegrationError(w, r, sourceErr)
		return
	}
	if err != nil {
		writeImportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func parseProjectRows(source string, rows [][]string) (imports.ParsedFile, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.WriteAll(rows); err != nil || buf.Len() > imports.MaxFileBytes {
		return imports.ParsedFile{}, imports.ErrInvalidRequest
	}
	parsed, err := imports.ParseJiraCSV(&buf)
	if err != nil {
		return imports.ParsedFile{}, err
	}
	parsed.Source = source
	return parsed, nil
}

func (h *Handler) listTAPDImportProjects(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Credentials integrations.ProviderTokenInput `json:"credentials"`
	}
	if err := decodeJSONLimit(w, r, 8<<10, &input); err != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var source integrations.ProjectSource
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		source, err = h.integrations.TAPDImportSource(ctx, tx, call.organizationID, call.selected.Actor, input.Credentials)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	projects, err := source.Projects(ctx)
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, projects)
}
