package collaboration

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type GitDefaults struct {
	AuthorNameMode  string `json:"author_name_mode"`
	AuthorEmailMode string `json:"author_email_mode"`
	AuthorName      string `json:"author_name"`
	AuthorEmail     string `json:"author_email"`
	CoAuthorEnabled *bool  `json:"co_author_enabled"`
}

type GitSettings struct {
	GitDefaults
	Version   uint64      `json:"version"`
	Effective GitDefaults `json:"effective"`
	CanManage bool        `json:"can_manage"`
}

type GitSettingsInput struct {
	GitDefaults
	Version uint64 `json:"version"`
}

var gitSettingsEmail = regexp.MustCompile(`^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$`)

func normalizeGitDefaults(input GitDefaults, organization bool) (GitDefaults, error) {
	input.AuthorName = strings.TrimSpace(input.AuthorName)
	input.AuthorEmail = strings.TrimSpace(input.AuthorEmail)
	switch input.AuthorNameMode {
	case "USER_AND_AGENT", "USER", "OWNER_AND_AGENT", "OWNER", "MACHINE", "MANUAL":
	case "INHERIT":
		if organization {
			return GitDefaults{}, invalidInput("organization Git settings cannot inherit")
		}
	default:
		return GitDefaults{}, invalidInput("invalid Git name source")
	}
	switch input.AuthorEmailMode {
	case "USER", "OWNER", "MACHINE", "MANUAL":
	case "INHERIT":
		if organization {
			return GitDefaults{}, invalidInput("organization Git settings cannot inherit")
		}
	default:
		return GitDefaults{}, invalidInput("invalid Git email source")
	}
	if len([]rune(input.AuthorName)) > 128 || len([]rune(input.AuthorEmail)) > 254 ||
		strings.IndexFunc(input.AuthorName+input.AuthorEmail, unicode.IsControl) >= 0 ||
		strings.ContainsAny(input.AuthorName, "<>") ||
		(input.AuthorEmail != "" && !gitSettingsEmail.MatchString(input.AuthorEmail)) ||
		(organization && input.CoAuthorEnabled == nil) ||
		(input.AuthorNameMode == "MANUAL" && input.AuthorName == "") ||
		(input.AuthorEmailMode == "MANUAL" && input.AuthorEmail == "") {
		return GitDefaults{}, invalidInput("invalid Git settings")
	}
	if input.AuthorNameMode != "MANUAL" {
		input.AuthorName = ""
	}
	if input.AuthorEmailMode != "MANUAL" {
		input.AuthorEmail = ""
	}
	return input, nil
}

func inheritGitDefaults(parent, child GitDefaults) GitDefaults {
	if child.AuthorNameMode != "INHERIT" {
		parent.AuthorNameMode = child.AuthorNameMode
		parent.AuthorName = child.AuthorName
	}
	if child.AuthorEmailMode != "INHERIT" {
		parent.AuthorEmailMode = child.AuthorEmailMode
		parent.AuthorEmail = child.AuthorEmail
	}
	if child.CoAuthorEnabled != nil {
		parent.CoAuthorEnabled = child.CoAuthorEnabled
	}
	return parent
}

func completeLegacyGitModes(input, current GitDefaults, organization bool) GitDefaults {
	input.AuthorName = strings.TrimSpace(input.AuthorName)
	input.AuthorEmail = strings.TrimSpace(input.AuthorEmail)
	if input.AuthorNameMode == "" {
		switch {
		case input.AuthorName == current.AuthorName:
			input.AuthorNameMode = current.AuthorNameMode
		case input.AuthorName != "":
			input.AuthorNameMode = "MANUAL"
		case organization:
			input.AuthorNameMode = "USER_AND_AGENT"
		default:
			input.AuthorNameMode = "INHERIT"
		}
	}
	if input.AuthorEmailMode == "" {
		switch {
		case input.AuthorEmail == current.AuthorEmail:
			input.AuthorEmailMode = current.AuthorEmailMode
		case input.AuthorEmail != "":
			input.AuthorEmailMode = "MANUAL"
		case organization:
			input.AuthorEmailMode = "USER"
		default:
			input.AuthorEmailMode = "INHERIT"
		}
	}
	return input
}

func (s Service) GetGitSettings(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor) (GitSettings, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return GitSettings{}, err
	}
	var item GitSettings
	err := tx.QueryRow(ctx, `SELECT author_name, author_email, co_author_enabled, version, author_name_mode, author_email_mode
		FROM openfox.organization_git_settings WHERE organization_id=openfox_private.current_organization_id()`).
		Scan(&item.AuthorName, &item.AuthorEmail, &item.CoAuthorEnabled, &item.Version, &item.AuthorNameMode, &item.AuthorEmailMode)
	if err != nil {
		return GitSettings{}, err
	}
	item.Effective = item.GitDefaults
	item.CanManage = canManageOrganization(actor.Role)
	if spaceID == "" {
		return item, nil
	}
	err = tx.QueryRow(ctx, `SELECT author_name, author_email, co_author_enabled, version, author_name_mode, author_email_mode
		FROM openfox.space_git_settings WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid`, spaceID).
		Scan(&item.AuthorName, &item.AuthorEmail, &item.CoAuthorEnabled, &item.Version, &item.AuthorNameMode, &item.AuthorEmailMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return GitSettings{}, ErrSpaceNotFound
	}
	if err != nil {
		return GitSettings{}, err
	}
	item.Effective = inheritGitDefaults(item.Effective, item.GitDefaults)
	return item, nil
}

func (s Service) PutGitSettings(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input GitSettingsInput) (GitSettings, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return GitSettings{}, err
	}
	if spaceID == "" && !canManageOrganization(actor.Role) {
		return GitSettings{}, ErrOrganizationForbidden
	}
	if input.Version == 0 {
		return GitSettings{}, ErrInvalidVersion
	}
	current, err := s.GetGitSettings(ctx, tx, organizationID, spaceID, actor)
	if err != nil {
		return GitSettings{}, err
	}
	values, err := normalizeGitDefaults(completeLegacyGitModes(input.GitDefaults, current.GitDefaults, spaceID == ""), spaceID == "")
	if err != nil {
		return GitSettings{}, err
	}
	query := `UPDATE openfox.organization_git_settings SET author_name=$1, author_email=$2, co_author_enabled=$3,
		version=version+1, updated_by=$4::uuid, updated_at=clock_timestamp(), author_name_mode=$6, author_email_mode=$7
		WHERE organization_id=openfox_private.current_organization_id() AND version=$5`
	args := []any{values.AuthorName, values.AuthorEmail, values.CoAuthorEnabled, actor.UserID, input.Version, values.AuthorNameMode, values.AuthorEmailMode}
	if spaceID != "" {
		query = `UPDATE openfox.space_git_settings SET author_name=$1, author_email=$2, co_author_enabled=$3,
			version=version+1, updated_by=$4::uuid, updated_at=clock_timestamp(), author_name_mode=$6, author_email_mode=$7
			WHERE organization_id=openfox_private.current_organization_id() AND version=$5 AND space_id=$8::uuid`
		args = append(args, spaceID)
	}
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return GitSettings{}, err
	}
	if tag.RowsAffected() != 1 {
		return GitSettings{}, ErrVersionConflict
	}
	return s.GetGitSettings(ctx, tx, organizationID, spaceID, actor)
}
