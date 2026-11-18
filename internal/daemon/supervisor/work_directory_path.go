package supervisor

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"

	"openfox/internal/workdirpath"
)

func (j *Journal) recordWorkDirectory(ctx context.Context, device string, run RunContext, path string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(canonical) || !workdirpath.ValidPath(canonical) {
		return workdirpath.ErrUnavailable
	}
	_, err = j.db.ExecContext(ctx, `INSERT INTO conversation_directory_facts
		(device_id,organization_id,space_id,conversation_id,agent_id,run_id,generation,path)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(device_id,organization_id,space_id,conversation_id,agent_id)
		DO UPDATE SET run_id=excluded.run_id,generation=excluded.generation,path=excluded.path`,
		device, run.OrganizationID, run.SpaceID, run.ConversationID, run.AgentID, run.RunID, run.Generation, canonical)
	if err != nil {
		return workdirpath.ErrUnavailable
	}
	return nil
}

func (j *Journal) WorkDirectoryPath(ctx context.Context, scope workdirpath.Scope) (string, error) {
	if j == nil || j.db == nil || !scope.Valid() {
		return "", workdirpath.ErrUnavailable
	}
	var path string
	err := j.db.QueryRowContext(ctx, `SELECT path FROM conversation_directory_facts
		WHERE device_id=? AND organization_id=? AND space_id=? AND conversation_id=? AND agent_id=? AND run_id=? AND generation=?`,
		scope.DeviceID, scope.OrganizationID, scope.SpaceID, scope.ConversationID, scope.AgentID, scope.RunID, scope.Generation).Scan(&path)
	if err != nil || !filepath.IsAbs(path) || !workdirpath.ValidPath(path) {
		return "", workdirpath.ErrUnavailable
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return "", workdirpath.ErrUnavailable
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", workdirpath.ErrUnavailable
	}
	return path, nil
}

func (s *Supervisor) WorkDirectoryPath(ctx context.Context, scope workdirpath.Scope) (string, error) {
	if s == nil || s.config.DeviceID != scope.DeviceID {
		return "", workdirpath.ErrUnavailable
	}
	return s.config.Journal.WorkDirectoryPath(ctx, scope)
}

func ReadWorkDirectoryPath(ctx context.Context, database string, scope workdirpath.Scope) (string, error) {
	if !filepath.IsAbs(database) {
		return "", workdirpath.ErrUnavailable
	}
	info, err := os.Lstat(database)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", workdirpath.ErrUnavailable
	}
	dsn := "file:" + (&url.URL{Path: database}).EscapedPath() + "?mode=ro&_pragma=busy_timeout(3000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", workdirpath.ErrUnavailable
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return (&Journal{db: db}).WorkDirectoryPath(ctx, scope)
}
