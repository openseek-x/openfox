package artifactreview

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/membership"
	"strings"
)

// ValidateInputs 在查询文件之前校验数量、来源、标识与批注的大小限制。
func ValidateInputs(inputs []Input) error {
	if len(inputs) > 10 {
		return ErrInvalid
	}
	raw, _ := json.Marshal(inputs)
	if len(raw) > MaxToolBytes {
		return ErrInvalid
	}
	count := 0
	seen := map[string]bool{}
	sources := map[string]bool{}
	for _, input := range inputs {
		if !membership.ValidateUUID(input.ID) || seen[input.ID] || len(input.SHA256) != 64 || strings.ToLower(input.SHA256) != input.SHA256 || len(input.Comments) == 0 {
			return ErrInvalid
		}
		seen[input.ID] = true
		artifact := membership.ValidateUUID(input.ArtifactID) && input.NodeID == "" && input.SpaceID == "" && input.NodeVersion == 0
		native := input.ArtifactID == "" && membership.ValidateUUID(input.NodeID) && membership.ValidateUUID(input.SpaceID) && input.NodeVersion > 0
		if !artifact && !native {
			return ErrInvalid
		}
		key := input.ArtifactID + input.NodeID
		if sources[key] {
			return ErrInvalid
		}
		sources[key] = true
		comments := map[string]bool{}
		for _, c := range input.Comments {
			count++
			if count > 20 || !membership.ValidateUUID(c.ThreadID) || comments[c.ThreadID] || strings.TrimSpace(c.Body) == "" || len(c.Body) > MaxQuoteBytes || len(c.Anchor.Quote) > MaxQuoteBytes || !ValidText(c.Body) || !ValidText(c.Anchor.Quote) {
				return ErrInvalid
			}
			comments[c.ThreadID] = true
		}
	}
	return nil
}

// Sources 只读取授权后的元数据。已经提交的相同消息重试不重新读取已变更的文件。
func (s Service) Sources(ctx context.Context, tx pgx.Tx, org, conversation, user, clientID string, inputs []Input) ([]File, error) {
	if err := ValidateInputs(inputs); err != nil {
		return nil, err
	}
	if _, err := (conversations.Service{}).AuthorizeInvocation(ctx, tx, org, conversation, user); err != nil {
		return nil, err
	}
	if clientID != "" {
		var hash *string
		err := tx.QueryRow(ctx, `SELECT file_context_hash FROM openfox.messages
 WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid
 AND author_user_id=$2::uuid AND client_message_id=$3`, conversation, user, strings.TrimSpace(clientID)).Scan(&hash)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil && hash != nil {
			raw, _ := json.Marshal(inputs)
			if *hash != Digest(string(raw)) {
				return nil, conversations.ErrDuplicateMessage
			}
			return nil, nil
		}
	}
	files := make([]File, len(inputs))
	for i, input := range inputs {
		input.ConversationID = conversation
		file, err := s.source(ctx, tx, org, input, user, false)
		if err != nil {
			return nil, err
		}
		files[i] = file
	}
	return files, nil
}

// ValidateFileInput 在事务外校验文件与锚点，并签入不可从 JSON 伪造的进程内校验戳。
// Bind 再次检查来源权限、哈希与版本；预校验以后改变输入或文件会被拒绝。
func ValidateFileInput(input Input, file File, text string) (Input, error) {
	if err := ValidateInputs([]Input{input}); err != nil {
		return Input{}, err
	}
	if file.SHA256 != input.SHA256 || Digest(text) != file.SHA256 || !ValidSource(text, file.MIMEType) {
		return Input{}, ErrInvalid
	}
	for _, comment := range input.Comments {
		if err := ValidateAnchor(text, file.MIMEType, comment.Anchor); err != nil {
			return Input{}, err
		}
	}
	input.validated = validationStamp(input, file)
	input.validatedVersion = file.Version
	return input, nil
}

func validationStamp(input Input, file File) string {
	raw, _ := json.Marshal(input)
	return Digest(string(raw) + "\x00" + file.SHA256 + "\x00" + file.MIMEType)
}
