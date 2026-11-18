package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"time"
)

// Teambition's SDK signs enterprise app access tokens locally with HS256.
func teambitionToken(in ProviderTokenInput, now time.Time) string {
	enc := base64.RawURLEncoding
	claims, _ := json.Marshal(map[string]any{"_appId": in.Account, "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()})
	payload := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(in.Secret))
	mac.Write([]byte(payload))
	return payload + "." + enc.EncodeToString(mac.Sum(nil))
}

type teambitionItem struct {
	ID, Name, OrganizationID, Content, Note, ProjectID, ParentTaskID, TfsID, ExecutorID string
	IsDone, IsArchived                                                                  bool
}

func (s ProjectSource) teambitionPages(ctx context.Context, path string, visit func(teambitionItem) error) error {
	q := url.Values{"pageSize": {"100"}}
	seen := map[string]bool{}
	for page := 0; page <= 50; page++ {
		var v struct {
			Result                      []teambitionItem
			NextPageToken, ErrorMessage string
			TotalSize                   int
		}
		if err := s.get(ctx, path, q, &v); err != nil {
			return err
		}
		if v.Result == nil || len(v.Result) > 100 || v.ErrorMessage != "" || v.TotalSize > 5000 {
			return ErrUpstreamUnavailable
		}
		for _, r := range v.Result {
			if !companyPattern.MatchString(r.ID) || seen[r.ID] || len(seen) >= 5000 {
				return ErrUpstreamUnavailable
			}
			seen[r.ID] = true
			if err := visit(r); err != nil {
				return err
			}
		}
		if v.NextPageToken == "" {
			return nil
		}
		if len(v.Result) == 0 || q.Get("pageToken") == v.NextPageToken {
			return ErrUpstreamUnavailable
		}
		q.Set("pageToken", v.NextPageToken)
	}
	return ErrInvalidRequest
}
