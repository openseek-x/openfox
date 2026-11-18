package hermes

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const Version = "0.23.1"
const Commit = "3b421578588243f920886e6fe79a6faa87ea026e"

// Generated from the official Git tree, PM Python artifact and locked wheels;
// see the R05 development record. Never take expected hashes from the install.
//
//go:embed trusted_build.json.gz
var trustedBuild []byte

type fileIdentity struct {
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
	Symlink bool   `json:"symlink"`
}

type buildManifest struct {
	Version, Commit, Python, Platform string
	Files                             map[string]fileIdentity `json:"files"`
}

var loadManifest = sync.OnceValues(func() (*buildManifest, error) {
	r, err := gzip.NewReader(bytes.NewReader(trustedBuild))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var result buildManifest
	if err := json.NewDecoder(io.LimitReader(r, 8<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if result.Commit != Commit || result.Version != Version || result.Python != "3.14.9" || result.Platform != "darwin/arm64" || len(result.Files) != 29608 {
		return nil, fmt.Errorf("Hermes trusted build manifest is invalid")
	}
	return &result, nil
})

type installation struct {
	Source, Python, Base, Venv, Site, RGRoot, RG string
}

func verifyInstallation(ctx context.Context, source, python string) (installation, error) {
	var out installation
	if !filepath.IsAbs(source) || !filepath.IsAbs(python) || strings.ContainsAny(source+python, "\n\r\x00") {
		return out, fmt.Errorf("Hermes requires absolute OPENFOX_HERMES_ROOT and OPENFOX_HERMES_PYTHON paths")
	}
	var err error
	if out.Source, err = filepath.EvalSymlinks(source); err != nil {
		return out, fmt.Errorf("Hermes verified source directory is unavailable")
	}
	out.Python = filepath.Clean(python)
	out.Venv = filepath.Dir(filepath.Dir(out.Python))
	out.Venv, err = filepath.EvalSymlinks(out.Venv)
	if err != nil || filepath.Base(filepath.Dir(out.Python)) != "bin" || filepath.Base(out.Python) != "python" {
		return out, fmt.Errorf("Hermes requires the verified venv bin/python entry")
	}
	out.Python = filepath.Join(out.Venv, "bin", "python")
	resolved, err := filepath.EvalSymlinks(out.Python)
	if err != nil || filepath.Base(resolved) != "python3.14" || filepath.Base(filepath.Dir(resolved)) != "bin" {
		return out, fmt.Errorf("Hermes verified Python 3.14.9 is unavailable")
	}
	out.Base = filepath.Dir(filepath.Dir(resolved))
	out.Site = filepath.Join(out.Venv, "lib", "python3.14", "site-packages")
	out.RGRoot = filepath.Join(filepath.Dir(out.Base), "ripgrep-15.2.0-aarch64-apple-darwin")
	out.RG = filepath.Join(out.RGRoot, "rg")
	data, err := os.ReadFile(filepath.Join(out.Venv, "pyvenv.cfg"))
	if err != nil || len(data) > 4096 {
		return out, fmt.Errorf("Hermes venv configuration is invalid")
	}
	config := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		name, value, ok := strings.Cut(line, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || config[name] != "" {
			return out, fmt.Errorf("Hermes venv configuration is invalid")
		}
		config[name] = value
	}
	if len(config) != 5 || config["home"] != filepath.Join(out.Base, "bin") || config["implementation"] != "CPython" || config["uv"] != "0.13.5" || config["version_info"] != "3.14.9" || config["include-system-site-packages"] != "false" {
		return out, fmt.Errorf("Hermes venv must use the approved isolated Python and dependencies")
	}
	manifest, err := loadManifest()
	if err != nil {
		return out, err
	}
	for name, root := range map[string]string{"source": out.Source, "python": out.Base, "site": out.Site, "rg": out.RGRoot} {
		if err := verifyTree(ctx, manifest, name, root); err != nil {
			return out, err
		}
	}
	return out, nil
}

func verifyTree(ctx context.Context, manifest *buildManifest, prefix, root string) error {
	seen := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("Hermes trusted %s files are unreadable", prefix)
		}
		if path == root {
			return nil
		}
		// Python is always launched with a fresh PYTHONPYCACHEPREFIX, and the
		// sandbox also denies pre-existing bytecode caches and source .git.
		if entry.IsDir() && (entry.Name() == "__pycache__" || prefix == "source" && entry.Name() == ".git") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// Wheel RECORD indexes contain generated console-script shebang hashes
		// that legitimately depend on the installation directory. They are not
		// executed or trusted and the native sandbox denies reading them.
		if prefix == "site" && filepath.Base(path) == "RECORD" && strings.HasSuffix(filepath.Base(filepath.Dir(path)), ".dist-info") {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("Hermes wheel index type is invalid")
			}
			return nil
		}
		identity, ok := manifest.Files[prefix+"/"+filepath.ToSlash(rel)]
		if !ok || entry.Type()&os.ModeSymlink != 0 != identity.Symlink {
			return fmt.Errorf("Hermes trusted %s tree has an unexpected or changed file", prefix)
		}
		var data []byte
		if identity.Symlink {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			data = []byte(link)
		} else {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() != identity.Bytes {
				return fmt.Errorf("Hermes trusted %s file size/type does not match", prefix)
			}
			data, err = os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("Hermes trusted %s file cannot be read", prefix)
			}
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != identity.SHA256 {
			return fmt.Errorf("Hermes trusted %s file digest does not match", prefix)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	expected := 0
	for path := range manifest.Files {
		if strings.HasPrefix(path, prefix+"/") {
			expected++
		}
	}
	if seen != expected {
		return fmt.Errorf("Hermes trusted %s tree is incomplete", prefix)
	}
	return nil
}
