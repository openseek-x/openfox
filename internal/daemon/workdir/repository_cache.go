package workdir

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"openfox/internal/platform/safehttp"
	"openfox/internal/reposource"
)

var gitOID = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
var cacheFilename = regexp.MustCompile(`^[a-f0-9]{64}\.bundle$`)

func hashedName(v any) string {
	data, _ := json.Marshal(v)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// An agent-configured remote must never inherit the device owner's SSH agent,
// credential helpers, URL rewrites, hooks, proxy or personal git configuration.
func repositoryGit(ctx context.Context, dir string, args ...string) (string, error) {
	return repositoryGitAuth(ctx, dir, reposource.Repository{}, args...)
}
func repositoryGitAuth(ctx context.Context, dir string, repo reposource.Repository, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "http.followRedirects=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:file", "GIT_LFS_SKIP_SMUDGE=1"}
	if auth := repo.Authorization; auth != nil {
		if !reposource.Valid(repo) || auth.Scope == "" || auth.Username == "" || auth.Token == "" {
			return "", ErrInvalidRequest
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Token))
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http."+repo.URL+".extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+encoded)
	}
	out := &limitedBuffer{limit: 65536}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || out.exceeded {
		return "", ErrRepositoryUnavailable
	}
	return strings.TrimSpace(out.String()), nil
}

func pinRemote(ctx context.Context, repo reposource.Repository) (string, error) {
	if !reposource.Valid(repo) {
		return "", ErrInvalidRequest
	}
	u, _ := url.Parse(repo.URL)
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(ips) == 0 {
		return "", ErrRepositoryUnavailable
	}
	for _, ip := range ips {
		if !repositoryIPAllowed(ip.IP, repo.Authorization != nil && repo.Authorization.PrivateNetwork) {
			return "", ErrPathOutsideAllowed
		}
	}
	addr := ips[0].IP.String()
	if strings.Contains(addr, ":") {
		addr = "[" + addr + "]"
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return "http.curloptResolve=" + u.Hostname() + ":" + port + ":" + addr, nil
}

// Intranet authorization permits private LAN addresses, never loopback,
// link-local metadata endpoints or multicast targets.
func repositoryIPAllowed(ip net.IP, intranet bool) bool {
	if ip.Equal(net.ParseIP("fd00:ec2::254")) || ip.Equal(net.ParseIP("fd00:ec2::23")) {
		return false
	}
	return safehttp.IPAllowed(ip) || (intranet && ip.IsPrivate())
}

// A cache entry is a self-contained read-only Git bundle. Independent clones
// keep their own objects, local changes and refs after the bundle is evicted.
func checkoutRepository(ctx context.Context, cache, stage, dest string, repo reposource.Repository) error {
	pin, err := pinRemote(ctx, repo)
	if err != nil {
		return err
	}
	ref := "HEAD"
	if repo.Branch != "" {
		if _, err := repositoryGit(ctx, stage, "check-ref-format", "--branch", repo.Branch); err != nil {
			return ErrInvalidRequest
		}
		ref = "refs/heads/" + repo.Branch
	}
	// Older Git versions silently ignore unknown -c options. Require support
	// for address pinning before allowing the first network request.
	configs, err := repositoryGit(ctx, stage, "help", "--config")
	if err != nil || !strings.Contains("\n"+strings.ToLower(configs)+"\n", "\nhttp.curloptresolve\n") {
		return ErrRepositoryUnavailable
	}
	output, err := repositoryGitAuth(ctx, stage, repo, "-c", pin, "ls-remote", "--symref", "--exit-code", repo.URL, ref)
	if err != nil {
		return err
	}
	branch := repo.Branch
	if branch == "" {
		symbolic, rest, ok := strings.Cut(output, "\n")
		fields := strings.Fields(symbolic)
		if !ok || len(fields) != 3 || fields[0] != "ref:" || fields[2] != "HEAD" || !strings.HasPrefix(fields[1], "refs/heads/") {
			return ErrRepositoryUnavailable
		}
		branch = strings.TrimPrefix(fields[1], "refs/heads/")
		if _, err := repositoryGit(ctx, stage, "check-ref-format", "--branch", branch); err != nil {
			return ErrRepositoryUnavailable
		}
		output = rest
	}
	parts := strings.Fields(output)
	if len(parts) != 2 || !gitOID.MatchString(parts[0]) || parts[1] != ref {
		return ErrRepositoryUnavailable
	}
	identity := []string{repo.URL, parts[0]}
	if scope := repositoryScope(repo); scope != "" {
		identity = append(identity, scope)
	}
	file := filepath.Join(cache, hashedName(identity)+".bundle")
	st, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		scratch, err := os.MkdirTemp(cache, "fetch-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(scratch)
		if _, err = repositoryGit(ctx, scratch, "init", "--bare", "."); err != nil {
			return err
		}
		if _, err = repositoryGitAuth(ctx, scratch, repo, "-c", pin, "fetch", "--no-tags", repo.URL, ref+":refs/heads/base"); err != nil {
			return err
		}
		actual, err := repositoryGit(ctx, scratch, "rev-parse", "refs/heads/base")
		if err != nil || actual != parts[0] {
			return ErrRepositoryUnavailable
		}
		temp := filepath.Join(scratch, "snapshot.bundle")
		if _, err = repositoryGit(ctx, scratch, "bundle", "create", temp, "refs/heads/base"); err != nil {
			return err
		}
		if err = os.Rename(temp, file); err != nil {
			return err
		}
	} else if err != nil || !st.Mode().IsRegular() {
		return ErrRepositoryUnavailable
	}
	if _, err = repositoryGit(ctx, stage, "init", "--", dest); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"remote", "add", "origin", repo.URL},
		{"fetch", "--no-tags", file, "refs/heads/base:refs/remotes/origin/" + branch},
		{"checkout", "-b", branch, "--track", "origin/" + branch},
	} {
		if _, err := repositoryGit(ctx, dest, args...); err != nil {
			return err
		}
	}
	now := time.Now()
	return os.Chtimes(file, now, now)
}

func (m *Manager) PrepareRepositories(ctx context.Context, key BindingKey, repos []reposource.Repository, cacheRoot string) (*PreparedWorkspace, error) {
	if !key.valid() || len(repos) == 0 || len(repos) > 64 {
		return nil, ErrInvalidRequest
	}
	// Serialize only this source workspace. Regular task preparations remain independent.
	identities := make([]any, len(repos))
	for i, r := range repos {
		identities[i] = r.Identity()
	}
	name := hashedName([]any{key, identities})
	if err := m.repoLock(ctx, "workspace:"+name); err != nil {
		return nil, err
	}
	defer m.releaseRepoLock("workspace:" + name)
	root, err := m.managedRoot()
	if err != nil {
		return nil, err
	}
	target := filepath.Join(root, "repos-"+name)
	if st, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		org := hashedName(key.OrganizationID)
		if err = m.repoLock(bounded, "cache:"+org); err != nil {
			return nil, err
		}
		defer m.releaseRepoLock("cache:" + org)
		cache := filepath.Join(cacheRoot, org)
		if err = os.MkdirAll(cache, 0700); err != nil {
			return nil, err
		}
		if real, err := canonicalPath(cache); err != nil || real != cache {
			return nil, ErrPathOutsideAllowed
		}
		stage, err := os.MkdirTemp(root, "repo-stage-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(stage)
		workspace := filepath.Join(stage, "checkout")
		if len(repos) > 1 {
			if err = os.Mkdir(workspace, 0700); err != nil {
				return nil, err
			}
		}
		for i, r := range repos {
			dest := workspace
			if len(repos) > 1 {
				u, err := url.Parse(r.URL)
				if err != nil {
					return nil, ErrInvalidRequest
				}
				dest = filepath.Join(workspace, fmt.Sprintf("%02d-%s", i+1, strings.TrimSuffix(filepath.Base(u.Path), ".git")))
			}
			if err := checkoutRepository(bounded, cache, stage, dest, r); err != nil {
				return nil, err
			}
		}
		if err = os.Rename(workspace, target); err != nil {
			return nil, err
		}
	} else if err != nil || !st.IsDir() {
		return nil, ErrPathOutsideAllowed
	} else {
		// A previous authorized checkout is not proof of current credential access.
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		for _, r := range repos {
			if r.Authorization == nil {
				continue
			}
			pin, e := pinRemote(bounded, r)
			if e != nil {
				return nil, e
			}
			if _, e = repositoryGitAuth(bounded, root, r, "-c", pin, "ls-remote", "--exit-code", r.URL, "HEAD"); e != nil {
				return nil, e
			}
		}
	}
	lease, err := m.acquire(ctx, target, key)
	if err != nil {
		return nil, err
	}
	return &PreparedWorkspace{Path: target, Owned: true, IsGit: len(repos) == 1, Binding: Binding{Key: key, Mode: ModeTempGit, ActualPath: target, Owned: true, IsGit: len(repos) == 1}, lease: lease}, nil
}

// Only regular, hash-named bundle files in this organization's private cache
// are disposable. A checkout or registered repository is never a candidate.
func (m *Manager) ExpireRepositoryCache(ctx context.Context, root, orgID string, cutoff time.Time) error {
	org := hashedName(orgID)
	if err := m.repoLock(ctx, "cache:"+org); err != nil {
		return err
	}
	defer m.releaseRepoLock("cache:" + org)
	path := filepath.Join(root, org)
	real, err := canonicalPath(path)
	if err != nil || real != path {
		return ErrPathOutsideAllowed
	}
	dir, err := os.OpenRoot(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	files, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer files.Close()
	for {
		entries, err := files.ReadDir(128)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !cacheFilename.MatchString(entry.Name()) || !entry.Type().IsRegular() {
				continue
			}
			info, err := dir.Lstat(entry.Name())
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
				if err = dir.Remove(entry.Name()); err != nil {
					return err
				}
			}
		}
	}
}

func repositoryScope(r reposource.Repository) string {
	if r.Authorization == nil {
		return ""
	}
	return r.Authorization.Scope
}
