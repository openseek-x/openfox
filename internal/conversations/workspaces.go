package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/policy"
)

type WorkspaceSelection struct {
	Mode                 string `json:"mode"`
	Directory            string `json:"directory,omitempty"`
	SourceConversationID string `json:"source_conversation_id,omitempty"`
	BaseRef              string `json:"base_ref,omitempty"`
}

// WorkspaceBinding adds center-derived provenance. It is never accepted in a client request.
type WorkspaceBinding struct {
	WorkspaceSelection
	ComputerID      string `json:"computer_id"`
	SourceAgentID   string `json:"source_agent_id,omitempty"`
	SourceDirectory string `json:"source_directory,omitempty"`
}

type WorkspaceOptions struct {
	DeviceID           string   `json:"device_id"`
	Supported          bool     `json:"supported"`
	Directories        []string `json:"directories"`
	GitDirectories     []string `json:"git_directories"`
	CanManageDirectory bool     `json:"can_manage_directory"`
	RecentDirectories  []string `json:"recent_directories"`
	CanReuse           bool     `json:"can_reuse"`
	CanSetBaseline     bool     `json:"can_set_baseline"`
}

type workspaceAgent struct {
	WorkspaceOptions
	spaceID     string
	builtin     string
	deviceOwner bool
	registered  []string
}

func validWorkspaceDirectory(s string) bool {
	if s == "" || len(s) > 1024 || strings.TrimSpace(s) != s || strings.HasPrefix(s, "~") ||
		strings.ContainsAny(s, "\\:") || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validBaseRef(ref string) bool {
	if ref == "" {
		return true
	}
	// External input denotes a single local branch or commit, never a Git refspec.
	if len(ref) > 255 || strings.IndexFunc(ref, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 ||
		(strings.HasPrefix(ref, "refs/") && !strings.HasPrefix(ref, "refs/heads/")) || strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "+") || ref == "@" || strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.ContainsAny(ref, "~^:?*[\\") || strings.HasSuffix(ref, ".") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func (s WorkspaceSelection) valid() bool {
	if !validBaseRef(s.BaseRef) {
		return false
	}
	switch s.Mode {
	case "DIRECT", "WORKTREE":
		return s.SourceConversationID == "" && validWorkspaceDirectory(s.Directory) && (s.Mode == "WORKTREE" || s.BaseRef == "")
	case "REUSE", "BASELINE":
		return s.Directory == "" && membership.ValidateUUID(s.SourceConversationID) && (s.Mode == "BASELINE" || s.BaseRef == "")
	}
	return false
}

// Aliases can themselves contain slashes. Resolve the longest registered alias
// before checking authorization so an allowed parent cannot shadow another root.
func (a workspaceAgent) allows(directory string) bool {
	if !validWorkspaceDirectory(directory) {
		return false
	}
	root := ""
	for _, candidate := range a.registered {
		if len(candidate) > len(root) && (directory == candidate || strings.HasPrefix(directory, candidate+"/")) {
			root = candidate
		}
	}
	return slices.Contains(a.Directories, root)
}

const workspaceAgentsSQL = `
		SELECT a.space_id::text,d.id::text,COALESCE(a.builtin_kind,''),COALESCE(d.owner_user_id=$2::uuid,false),
		       cfg.directory_mode,cfg.directory_roots,jsonb_build_object(
		           'conversation_workdir',d.capabilities->'conversation_workdir',
		           'conversation_workdir_roots',d.capabilities->'conversation_workdir_roots',
		           'conversation_workdir_git_roots',d.capabilities->'conversation_workdir_git_roots')
		  FROM openfox.agents a
		  JOIN openfox.agent_config_versions cfg ON cfg.organization_id=a.organization_id AND cfg.agent_id=a.id AND cfg.config_version=a.current_config_version
		  JOIN openfox.physical_devices d ON d.id=cfg.computer_id AND d.id=a.device_id AND d.status='ACTIVE'
		  JOIN openfox.device_grants g ON g.organization_id=a.organization_id AND g.id=cfg.grant_id AND g.device_id=d.id
		   AND g.status='ACTIVE' AND (g.scope<>'PERSONAL' OR g.scope_user_id=$2::uuid)
		   AND (g.scope<>'SPACE' OR g.scope_space_id=a.space_id)
		 WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=ANY($1::uuid[]) AND a.status='ACTIVE' AND a.archived_at IS NULL
		   AND (a.visibility='SHARED' OR a.owner_user_id=$2::uuid)`

func loadWorkspaceAgent(ctx context.Context, tx pgx.Tx, agentID, userID string) (workspaceAgent, error) {
	return scanWorkspaceAgent(tx.QueryRow(ctx, workspaceAgentsSQL, []string{agentID}, userID))
}

func scanWorkspaceAgent(row pgx.Row) (workspaceAgent, error) {
	var agent workspaceAgent
	var mode string
	var cfgRoots []string
	var capabilities []byte
	err := row.Scan(&agent.spaceID, &agent.DeviceID, &agent.builtin, &agent.deviceOwner, &mode, &cfgRoots, &capabilities)
	if errors.Is(err, pgx.ErrNoRows) {
		return agent, ErrNotFound
	}
	if err != nil {
		return agent, err
	}
	agent.Directories = []string{}
	agent.GitDirectories = []string{}
	var caps struct {
		Supported bool     `json:"conversation_workdir"`
		Roots     []string `json:"conversation_workdir_roots"`
		GitRoots  []string `json:"conversation_workdir_git_roots"`
	}
	if json.Unmarshal(capabilities, &caps) != nil {
		return agent, nil
	}
	agent.Supported = caps.Supported
	agent.registered = caps.Roots
	if mode == "EXPLICIT" && agent.Supported {
		for _, root := range cfgRoots {
			if validWorkspaceDirectory(root) && slices.Contains(caps.Roots, root) && !slices.Contains(agent.Directories, root) {
				agent.Directories = append(agent.Directories, root)
				if slices.Contains(caps.GitRoots, root) {
					agent.GitDirectories = append(agent.GitDirectories, root)
				}
			}
		}
	}
	return agent, nil
}

type workspaceSource struct {
	directory, agentID, computerID string
	worktree                       bool
}

// Only runs which actually started can have a reusable workspace. An old default
// run has a known source only when it selected exactly one explicit logical root.
func sourceWorkspace(ctx context.Context, tx pgx.Tx, sourceID string) (workspaceSource, error) {
	var src workspaceSource
	err := tx.QueryRow(ctx, `
		SELECT r.agent_id::text,r.computer_id::text,COALESCE(
			r.workspace_selection->>'source_directory', r.workspace_selection->>'directory',
			CASE WHEN cfg.directory_mode='EXPLICIT' AND jsonb_array_length(cfg.directory_roots)=1 THEN cfg.directory_roots->>0 END,''), COALESCE(r.workspace_selection->>'mode' IN ('WORKTREE','BASELINE'),cfg.workdir_isolation='WORKTREE')
		  FROM openfox.runs r JOIN openfox.agent_config_versions cfg ON cfg.organization_id=r.organization_id
		   AND cfg.agent_id=r.agent_id AND cfg.config_version=r.config_version
		 WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid AND r.started_at IS NOT NULL
		 ORDER BY r.created_at DESC,r.id LIMIT 1`, sourceID).Scan(&src.agentID, &src.computerID, &src.directory, &src.worktree)
	if errors.Is(err, pgx.ErrNoRows) {
		return src, ErrNotFound
	}
	return src, err
}

func (s Service) WorkspaceOptions(ctx context.Context, tx pgx.Tx, orgID string, actor Actor, agentID, sourceID string) (WorkspaceOptions, error) {
	if !membership.ValidateUUID(agentID) || (sourceID != "" && !membership.ValidateUUID(sourceID)) {
		return WorkspaceOptions{}, ErrInvalidRequest
	}
	if err := s.CheckActor(ctx, tx, orgID, actor); err != nil {
		return WorkspaceOptions{}, err
	}
	agent, err := loadWorkspaceAgent(ctx, tx, agentID, actor.UserID)
	if err != nil {
		return WorkspaceOptions{}, err
	}
	if err = s.authorizeSpace(ctx, tx, orgID, actor, agent.spaceID, agentID, policy.ActionCreateObject); err != nil {
		return WorkspaceOptions{}, err
	}
	switch agent.builtin {
	case BuiltinAssistant:
		agent.CanManageDirectory = agent.deviceOwner && agent.Supported
	case "":
		agent.CanManageDirectory = s.authorizeSpace(ctx, tx, orgID, actor, agent.spaceID, agentID, policy.ActionManageSpace) == nil
	}
	agent.RecentDirectories, err = recentWorkDirectories(ctx, tx, orgID, actor.UserID, agentID, agent.DeviceID)
	if err != nil {
		return WorkspaceOptions{}, err
	}
	agent.RecentDirectories = slices.DeleteFunc(agent.RecentDirectories, func(directory string) bool { return !agent.allows(directory) })
	if sourceID != "" {
		source, err := s.AuthorizeWrite(ctx, tx, orgID, sourceID, actor)
		if err != nil {
			return WorkspaceOptions{}, err
		}
		if source.TaskID != "" && source.SpaceID == agent.spaceID {
			src, err := s.eligibleWorkspaceSource(ctx, tx, orgID, actor.UserID, source, agent)
			switch {
			case err == nil:
				agent.CanReuse = true
				agent.CanSetBaseline = src.worktree || slices.Contains(agent.GitDirectories, src.directory)
			case errors.Is(err, ErrWorkspaceUnavailable):
			default:
				return WorkspaceOptions{}, err
			}
		}
	}
	return agent.WorkspaceOptions, nil
}

func (s Service) bindWorkspace(ctx context.Context, tx pgx.Tx, orgID string, actor Actor, input CreateInput) (*WorkspaceBinding, error) {
	request := input.WorkspaceSelection
	if request == nil {
		return nil, nil
	}
	if !request.valid() {
		return nil, ErrInvalidRequest
	}
	agent, err := loadWorkspaceAgent(ctx, tx, input.AgentID, actor.UserID)
	if err != nil {
		return nil, err
	}
	if !agent.Supported {
		return nil, ErrWorkspaceUnavailable
	}
	b := WorkspaceBinding{WorkspaceSelection: *request, ComputerID: agent.DeviceID}
	if request.SourceConversationID == "" {
		if !agent.allows(request.Directory) {
			return nil, ErrForbidden
		}
		// The heartbeat can rule out a non-Git registered root. A native-picked
		// subdirectory can be its own repository; the device verifies its exact path.
		if request.Mode == "WORKTREE" && slices.Contains(agent.Directories, request.Directory) && !slices.Contains(agent.GitDirectories, request.Directory) {
			return nil, ErrWorkspaceUnavailable
		}
		return &b, nil
	}
	source, err := s.AuthorizeWrite(ctx, tx, orgID, request.SourceConversationID, actor)
	if err != nil {
		return nil, err
	}
	if input.TaskID == "" || source.TaskID != input.TaskID || source.SpaceID != input.SpaceID || source.ID == input.ID {
		return nil, ErrForbidden
	}
	src, err := s.eligibleWorkspaceSource(ctx, tx, orgID, actor.UserID, source, agent)
	if err != nil {
		return nil, err
	}
	if request.Mode == "BASELINE" && !src.worktree && slices.Contains(agent.Directories, src.directory) && !slices.Contains(agent.GitDirectories, src.directory) {
		return nil, ErrWorkspaceUnavailable
	}
	b.SourceAgentID, b.SourceDirectory = src.agentID, src.directory
	return &b, nil
}

func (s Service) eligibleWorkspaceSource(ctx context.Context, tx pgx.Tx, orgID, userID string, source Conversation, target workspaceAgent) (workspaceSource, error) {
	src, err := sourceWorkspace(ctx, tx, source.ID)
	if errors.Is(err, ErrNotFound) {
		return src, ErrWorkspaceUnavailable
	}
	if err != nil {
		return src, err
	}
	if src.computerID != target.DeviceID || !target.allows(src.directory) {
		return src, ErrWorkspaceUnavailable
	}
	allowed, err := s.sourceChainAllowed(ctx, tx, orgID, userID, src, source)
	if err != nil {
		return src, err
	}
	if !allowed {
		return src, ErrWorkspaceUnavailable
	}
	return src, nil
}

// Reusing B, which itself reuses A, requires access to the same ancestors as
// running B. Otherwise a shared intermediate conversation could bypass a
// revoked membership. Stop at 32 sources to bound authorization work.
func (s Service) sourceChainAllowed(ctx context.Context, tx pgx.Tx, orgID, userID string, src workspaceSource, source Conversation) (bool, error) {
	if source.WorkspaceSelection == nil || source.WorkspaceSelection.SourceConversationID == "" {
		agent, err := loadWorkspaceAgent(ctx, tx, src.agentID, userID)
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return agent.DeviceID == src.computerID && agent.allows(src.directory), nil
	}
	// All ancestors must be in the already authorized task/space. Evaluate
	// their participation together rather than issuing permission reads per hop.
	var agents []string
	var allowed bool
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE lineage(id,agent_id,depth) AS (
			SELECT $1::uuid,$2::uuid,1
			UNION ALL
			SELECT (c.workspace_selection->>'source_conversation_id')::uuid,
			       (c.workspace_selection->>'source_agent_id')::uuid,l.depth+1
			  FROM lineage l JOIN openfox.conversations c
			    ON c.organization_id=openfox_private.current_organization_id() AND c.id=l.id
			 WHERE l.depth<32 AND c.workspace_selection ? 'source_conversation_id'
		)
		SELECT ARRAY(SELECT DISTINCT agent_id::text FROM lineage),
		       COALESCE(bool_and(c.id IS NOT NULL AND c.task_id IS NOT DISTINCT FROM $3::uuid AND c.space_id=$4::uuid AND c.lifecycle='OPEN'
		          AND (c.owner_user_id=$5::uuid OR (c.visibility='SHARED' AND cm.access_level IS NOT DISTINCT FROM 'COMMENTER'))
		          AND (NOT COALESCE(c.workspace_selection ? 'source_conversation_id',false)
		               OR (c.workspace_selection->>'computer_id'=$6 AND c.workspace_selection->>'source_directory'=$7))),false)
		       AND COALESCE(bool_or(c.id IS NOT NULL AND NOT COALESCE(c.workspace_selection ? 'source_conversation_id',false)),false)
		  FROM lineage l LEFT JOIN openfox.conversations c
		    ON c.organization_id=openfox_private.current_organization_id() AND c.id=l.id
		  LEFT JOIN openfox.conversation_members cm ON cm.organization_id=c.organization_id AND cm.conversation_id=c.id AND cm.user_id=$5::uuid`,
		source.ID, src.agentID, source.TaskID, source.SpaceID, userID, src.computerID, src.directory).Scan(&agents, &allowed)
	if err != nil || !allowed {
		return false, err
	}
	rows, err := tx.Query(ctx, workspaceAgentsSQL, agents, userID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		agent, err := scanWorkspaceAgent(rows)
		if err != nil {
			return false, err
		}
		if agent.DeviceID != src.computerID || !agent.allows(src.directory) {
			return false, nil
		}
		count++
	}
	return count == len(agents), rows.Err()
}

var ErrWorkspaceUnavailable = errors.New("conversations: workspace unavailable on this device")

// CheckWorkspaceBinding shares the live ACL checks across planning, offers, context,
// start and renewal. Configuration versions freeze execution defaults, not access.
func (s Service) CheckWorkspaceBinding(ctx context.Context, tx pgx.Tx, orgID, userID, agentID, deviceID, taskID, spaceID string, b *WorkspaceBinding) (bool, error) {
	if b == nil {
		return true, nil
	}
	if b.ComputerID != deviceID {
		return false, nil
	}
	agent, err := loadWorkspaceAgent(ctx, tx, agentID, userID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if agent.DeviceID != deviceID || agent.spaceID != spaceID {
		return false, nil
	}
	if b.SourceConversationID == "" {
		return agent.allows(b.Directory), nil
	}
	source, err := s.AuthorizeInvocation(ctx, tx, orgID, b.SourceConversationID, userID)
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if source.TaskID == "" || source.TaskID != taskID || source.SpaceID != spaceID || source.Lifecycle != LifecycleOpen || !agent.allows(b.SourceDirectory) {
		return false, nil
	}
	return s.sourceChainAllowed(ctx, tx, orgID, userID, workspaceSource{agentID: b.SourceAgentID, computerID: deviceID, directory: b.SourceDirectory}, source)
}
