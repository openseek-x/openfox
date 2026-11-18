package teams

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

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
		"icons":       map[string]string{"color": "color.png", "outline": "outline.png"}, "accentColor": "#139948",
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
	}{{"manifest.json", manifest}, {"color.png", brandIcon(192, false)}, {"outline.png", brandIcon(32, true)}} {
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

// Rasterize the existing OpenFox brand-logo geometry from packages/ui/icons.
// No downloaded artwork or dependency is involved. Outline is white/transparent.
func brandIcon(size int, outline bool) []byte {
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	green := color.NRGBA{R: 0x13, G: 0x99, B: 0x48, A: 255}
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	fox := [][2]float64{{6.5, 8.5}, {11, 12.5}, {17, 12.5}, {21.5, 8.5}, {21.5, 15.5}, {14, 21.5}, {6.5, 15.5}}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := (float64(x)+0.5)*28/float64(size), (float64(y)+0.5)*28/float64(size)
			if !outline {
				dx, dy := 0.0, 0.0
				if px < 8 {
					dx = 8 - px
				} else if px > 20 {
					dx = px - 20
				}
				if py < 8 {
					dy = 8 - py
				} else if py > 20 {
					dy = py - 20
				}
				if dx*dx+dy*dy <= 64 {
					out.SetNRGBA(x, y, green)
				}
			}
			inside := false
			for i, j := 0, len(fox)-1; i < len(fox); j, i = i, i+1 {
				a, b := fox[i], fox[j]
				if (a[1] > py) != (b[1] > py) && px < (b[0]-a[0])*(py-a[1])/(b[1]-a[1])+a[0] {
					inside = !inside
				}
			}
			if inside {
				out.SetNRGBA(x, y, white)
			}
			for _, cx := range []float64{11.2, 16.8} {
				if (px-cx)*(px-cx)+(py-15.2)*(py-15.2) < 1.44 {
					if outline {
						out.SetNRGBA(x, y, color.NRGBA{})
					} else {
						out.SetNRGBA(x, y, green)
					}
				}
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	return buf.Bytes()
}
