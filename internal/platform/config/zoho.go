package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type ZohoOAuthApp struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}
type ZohoOAuthApps struct {
	Region       string       `json:"region"`
	Organization ZohoOAuthApp `json:"organization"`
	Identity     ZohoOAuthApp `json:"identity"`
}

func readZohoOAuthApps(path string) (ZohoOAuthApps, error) {
	var apps ZohoOAuthApps
	invalid := errors.New("integrations.zoho_oauth_apps_file: requires an absolute regular 0600 JSON file with distinct US organization and identity applications")
	if !filepath.IsAbs(path) {
		return apps, invalid
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		return apps, invalid
	}
	f, e := os.Open(path)
	if e != nil {
		return apps, invalid
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return apps, invalid
	}
	raw, e := io.ReadAll(io.LimitReader(f, 16385))
	if e != nil || len(raw) > 16384 {
		return apps, invalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&apps) != nil || dec.Decode(new(any)) != io.EOF || apps.Region != "US" || apps.Organization.ClientID == apps.Identity.ClientID {
		return ZohoOAuthApps{}, invalid
	}
	for _, app := range []ZohoOAuthApp{apps.Organization, apps.Identity} {
		for _, value := range []string{app.ClientID, app.ClientSecret} {
			if len(value) < 1 || len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
				return ZohoOAuthApps{}, invalid
			}
		}
	}
	return apps, nil
}
