package teams

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeamsManifestPackage(t *testing.T) {
	cfg := ManifestConfig{Version: "1.0.0", DeveloperName: "Controlled developer", WebsiteURL: "https://example.test/support", PrivacyURL: "https://example.test/privacy", TermsURL: "https://example.test/terms"}
	app := "11111111-1111-4111-8111-111111111111"
	data, err := ManifestZIP(app, "OpenFox fixture", cfg)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 3 {
		t.Fatal("expected three root entries")
	}
	files := map[string][]byte{}
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		_ = r.Close()
		if e != nil {
			t.Fatal(e)
		}
		files[f.Name] = b
	}
	var m map[string]any
	if json.Unmarshal(files["manifest.json"], &m) != nil {
		t.Fatal("invalid manifest")
	}
	if m["manifestVersion"] != "1.30" || m["id"] != app {
		t.Fatal("identity/version")
	}
	perms := m["authorization"].(map[string]any)["permissions"].(map[string]any)["resourceSpecific"].([]any)
	if len(perms) != 1 || perms[0].(map[string]any)["name"] != "ChannelSettings.Read.Group" {
		t.Fatal("unexpected permission")
	}
	if len(m["bots"].([]any)) != 1 || len(m["webApplicationInfo"].(map[string]any)) != 1 {
		t.Fatal("extra bot or SSO contract")
	}
	for name, size := range map[string]int{"color.png": 192, "outline.png": 32} {
		img, e := png.Decode(bytes.NewReader(files[name]))
		if e != nil || img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Fatal("bad PNG", name, e)
		}
		if name == "outline.png" {
			var white, clear int
			for y := 0; y < size; y++ {
				for x := 0; x < size; x++ {
					r, g, b, a := img.At(x, y).RGBA()
					if a == 0 {
						clear++
					} else if a == 65535 && r == 65535 && g == 65535 && b == 65535 {
						white++
					} else {
						t.Fatal("outline must be transparent/white")
					}
				}
			}
			if white == 0 || clear == 0 {
				t.Fatal("empty outline")
			}
		}
	}
	if strings.Contains(string(files["manifest.json"]), "secret") {
		t.Fatal("secret in manifest")
	}
	if dir := os.Getenv("OPENFOX_I16_MANIFEST_FIXTURE_DIR"); dir != "" {
		if !strings.HasPrefix(filepath.Clean(dir), "/private/tmp/openfox-i16-") {
			t.Fatal("fixture output must be exclusive tmp")
		}
		for name, b := range files {
			if e := os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
}
func TestTeamsManifestRejectsInvalidInputs(t *testing.T) {
	cfg := ManifestConfig{Version: "1.0.0", DeveloperName: "Fixture", WebsiteURL: "https://example.test", PrivacyURL: "https://example.test/privacy", TermsURL: "https://example.test/terms"}
	for _, field := range []string{"version", "developer", "credentialsURL", "httpURL", "blankURL", "longName"} {
		t.Run(field, func(t *testing.T) {
			v := cfg
			name := "Fixture"
			switch field {
			case "version":
				v.Version = "1.0"
			case "developer":
				v.DeveloperName = ""
			case "credentialsURL":
				v.WebsiteURL = "https://user:secret@example.test"
			case "httpURL":
				v.PrivacyURL = "http://example.test"
			case "blankURL":
				v.TermsURL = ""
			case "longName":
				name = strings.Repeat("界", 31)
			}
			if _, err := Manifest("11111111-1111-4111-8111-111111111111", name, v); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
