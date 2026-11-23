package teams

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

//go:embed icons/color.png
var colorIcon []byte

//go:embed icons/outline.png
var outlineIcon []byte

type ManifestConfig struct {
	Version       string `json:"version"`
	DeveloperName string `json:"developer_name"`
	WebsiteURL    string `json:"website_url"`
	PrivacyURL    string `json:"privacy_url"`
	TermsURL      string `json:"terms_url"`
}

var manifestVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func (v ManifestConfig) Validate() error {
	if !manifestVersion.MatchString(v.Version) || len(v.Version) > 64 || !utf8.ValidString(v.DeveloperName) || strings.TrimSpace(v.DeveloperName) != v.DeveloperName || utf8.RuneCountInString(v.DeveloperName) < 1 || utf8.RuneCountInString(v.DeveloperName) > 32 {
		return ErrInvalid
	}
	for _, raw := range []string{v.WebsiteURL, v.PrivacyURL, v.TermsURL} {
		u, err := url.Parse(raw)
		if err != nil || len(raw) > 2048 || strings.TrimSpace(raw) != raw || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return ErrInvalid
		}
	}
	return nil
}
func Manifest(appID, name string, cfg ManifestConfig) ([]byte, error) {
	if !GUID(appID) || cfg.Validate() != nil || !utf8.ValidString(name) || strings.TrimSpace(name) != name || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 30 {
		return nil, ErrInvalid
	}
	v := map[string]any{
		"$schema":         "https://developer.microsoft.com/en-us/json-schemas/teams/v1.30/MicrosoftTeams.schema.json",
		"manifestVersion": "1.30", "version": cfg.Version, "id": appID,
		"developer":   map[string]string{"name": cfg.DeveloperName, "websiteUrl": cfg.WebsiteURL, "privacyUrl": cfg.PrivacyURL, "termsOfUseUrl": cfg.TermsURL},
		"name":        map[string]string{"short": name},
		"description": map[string]string{"short": "OpenFox task assistant and Agent conversations", "full": "Use your explicitly linked OpenFox identity to talk to this Agent. Group and standard channel replies require an OpenFox manager to enable the scope. Task permissions continue to apply."},
		"icons":       map[string]string{"color": "color.png", "outline": "outline.png"}, "accentColor": "#ED4200",
		"bots":               []any{map[string]any{"botId": appID, "scopes": []string{"personal", "groupChat", "team"}, "isNotificationOnly": false, "supportsFiles": false, "supportsCalling": false, "supportsVideo": false}},
		"authorization":      map[string]any{"permissions": map[string]any{"resourceSpecific": []any{map[string]string{"name": "ChannelSettings.Read.Group", "type": "Application"}}}},
		"webApplicationInfo": map[string]string{"id": appID},
	}
	return json.MarshalIndent(v, "", "  ")
}
func ManifestZIP(appID, name string, cfg ManifestConfig) ([]byte, error) {
	manifest, err := Manifest(appID, name, cfg)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"manifest.json", manifest}, {"color.png", colorIcon}, {"outline.png", outlineIcon}} {
		w, e := z.Create(entry.name)
		if e != nil {
			return nil, e
		}
		if _, e = w.Write(entry.data); e != nil {
			return nil, e
		}
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
