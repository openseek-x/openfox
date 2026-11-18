package integrations

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

type codeCommitCursor struct {
	ConnectionID string `json:"connection"`
	Version      uint64 `json:"version"`
	Next         string `json:"next"`
	Expires      int64  `json:"expires"`
}

func (s Service) DecodeCodeCommitCursor(org string, a CodeCommitAccess, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 16000 {
		return "", ErrInvalidRequest
	}
	blob, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil {
		return "", ErrInvalidRequest
	}
	plain, e := s.open(blob, "codecommit-repositories:"+org+":"+a.ID)
	if e != nil {
		return "", ErrInvalidRequest
	}
	var c codeCommitCursor
	if json.Unmarshal(plain, &c) != nil || c.ConnectionID != a.ID || c.Version != a.Version || c.Expires <= time.Now().Unix() || c.Next == "" || len(c.Next) > 8192 {
		return "", ErrInvalidRequest
	}
	return c.Next, nil
}
func (s Service) EncodeCodeCommitCursor(org string, a CodeCommitAccess, next string) (string, error) {
	if next == "" {
		return "", nil
	}
	if len(next) > 8192 {
		return "", ErrInvalidRequest
	}
	plain, _ := json.Marshal(codeCommitCursor{a.ID, a.Version, next, time.Now().Add(10 * time.Minute).Unix()})
	blob, _, e := s.seal(plain, "codecommit-repositories:"+org+":"+a.ID)
	if e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(blob), nil
}
