package agents

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"openfox/internal/tenancy"
)

type AssistantDirectorySettings struct {
	DeviceID    string   `json:"device_id"`
	Version     uint64   `json:"version"`
	Directories []string `json:"directories"`
	Directory   string   `json:"directory"`
}

type AssistantDirectoryInput struct {
	DeviceID  string `json:"device_id"`
	Version   uint64 `json:"version"`
	Directory string `json:"directory"`
}

// The assistant and the registered computer must both belong to the caller.
// Directory choices in a conversation never enter this configuration path.
func (s PostgresStore) AssistantDirectory(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, change *AssistantDirectoryInput) (AssistantDirectorySettings, error) {
	var result AssistantDirectorySettings
	if err := s.validate(selection, actor); err != nil {
		return result, err
	}
	err := s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		current, found, err := lockAssistant(ctx, tx, selection.OrganizationID, actor.UserID)
		if err != nil {
			return err
		}
		if !found || current.Archived {
			return ErrNotFound
		}
		result.DeviceID, result.Version = current.ComputerID, current.Version
		if current.DirectoryMode == DirectoryExplicit && len(current.DirectoryRoots) == 1 {
			result.Directory = current.DirectoryRoots[0]
		}
		err = tx.QueryRow(ctx, `SELECT ARRAY(SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(d.capabilities->'conversation_workdir_roots')='array'
    THEN d.capabilities->'conversation_workdir_roots' ELSE '[]'::jsonb END) ORDER BY 1)
    FROM openfox.physical_devices d JOIN openfox.device_grants g ON g.device_id=d.id
    WHERE d.id=$1 AND g.id=$2 AND g.organization_id=$3 AND d.owner_user_id=$4 AND d.status='ACTIVE' AND g.status='ACTIVE'
      AND d.capabilities->'conversation_workdir'='true'::jsonb FOR SHARE OF d,g`, current.ComputerID, current.GrantID, selection.OrganizationID, actor.UserID).Scan(&result.Directories)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil || change == nil {
			return err
		}
		if change.Version != current.Version || change.DeviceID != current.ComputerID {
			return ErrVersionConflict
		}
		if change.Directory != "" && !slices.Contains(result.Directories, change.Directory) {
			return ErrForbidden
		}
		if result.Directory == change.Directory && (current.DirectoryMode == DirectoryNone || len(current.DirectoryRoots) == 1) {
			return nil
		}
		item, err := loadAssistant(ctx, tx, selection.OrganizationID, actor.UserID)
		if err != nil {
			return err
		}
		c := item.Config
		if c == nil {
			return ErrInvalidConfig
		}
		config := ConfigInput{Instructions: c.Instructions, ComputerID: c.ComputerID, GrantID: c.GrantID, Runtime: c.Runtime, Model: c.Model,
			MaxConcurrency: c.MaxConcurrency, QueueTimeoutSeconds: c.QueueTimeoutSeconds, RunTimeoutSeconds: c.RunTimeoutSeconds,
			DirectoryMode: DirectoryNone, DirectoryRoots: []string{}, RunSettings: c.RunSettings}
		if change.Directory != "" {
			config.DirectoryMode = DirectoryExplicit
			config.DirectoryRoots = []string{change.Directory}
			config.WorkdirIsolation = "DIRECT"
		} else {
			config.WorkdirIsolation = "WORKTREE"
		}
		config, err = normalizeConfigInput(config)
		if err != nil {
			return err
		}
		if err = updateAssistant(ctx, tx, selection, actor, current, config); err != nil {
			return err
		}
		result.Version++
		result.Directory = change.Directory
		return nil
	})
	return result, err
}
