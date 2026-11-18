package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	localbrowser "openfox/internal/daemon/browser"
	"openfox/internal/membership"
	"openfox/internal/runtime"
)

const browserRunFileBytes = 100 << 20

// Files are snapshotted before Chromium receives a path. Keeping the snapshot
// until Run completion also lets a later form submit read the selected file.
type browserFiles struct {
	mu                                      sync.Mutex
	roots                                   []string
	artifactDir, transferRoot, downloadRoot string
	used                                    int64
	directories                             []string
	downloads                               map[string]string
	closed                                  bool
}

func (f *browserFiles) invoke(ctx context.Context, host localbrowser.Host, request localbrowser.Request) runtime.ToolResult {
	if request.Tool != "openfox_browser_files" {
		return host.Execute(ctx, request)
	}
	var args struct {
		Action string   `json:"action"`
		Paths  []string `json:"paths"`
		FileID string   `json:"file_id"`
	}
	if json.Unmarshal(request.Arguments, &args) != nil {
		return runtime.TextResult("INVALID_REQUEST", false)
	}
	if args.Action == "list" {
		return host.Execute(ctx, request)
	}
	if request.ReadOnly {
		return runtime.TextResult("FORBIDDEN", false)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || ctx.Err() != nil {
		return runtime.TextResult("CONTROL_REVOKED", false)
	}
	if args.Action == "upload" {
		if len(args.Paths) < 1 || len(args.Paths) > 10 || !filepath.IsAbs(f.transferRoot) {
			return runtime.TextResult("INVALID_REQUEST", false)
		}
		if err := os.MkdirAll(f.transferRoot, 0700); err != nil {
			return runtime.TextResult("UNAVAILABLE", false)
		}
		info, err := os.Lstat(f.transferRoot)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return runtime.TextResult("UNAVAILABLE", false)
		}
		dir, err := os.MkdirTemp(f.transferRoot, "upload-")
		if err != nil {
			return runtime.TextResult("UNAVAILABLE", false)
		}
		keep := false
		defer func() {
			if !keep {
				_ = os.RemoveAll(dir)
			}
		}()
		var total int64
		for index, raw := range args.Paths {
			if ctx.Err() != nil {
				return runtime.TextResult("CONTROL_REVOKED", false)
			}
			file, err := inspectArtifact(raw, f.roots, maxArtifactFileBytes)
			if err != nil {
				return runtime.TextResult("FILE_REFUSED: choose a regular, non-secret file inside this Run's workspace or artifact directory", false)
			}
			total += int64(len(file.Content))
			if f.used+total > browserRunFileBytes {
				return runtime.TextResult("LIMIT_EXCEEDED", false)
			}
			folder := filepath.Join(dir, fmt.Sprint(index))
			if os.Mkdir(folder, 0700) != nil {
				return runtime.TextResult("UNAVAILABLE", false)
			}
			staged := filepath.Join(folder, file.Name)
			if os.WriteFile(staged, file.Content, 0600) != nil {
				return runtime.TextResult("UNAVAILABLE", false)
			}
			request.Files = append(request.Files, localbrowser.StagedFile{Path: staged, Name: file.Name, Size: int64(len(file.Content)), SHA256: file.SHA256})
		}
		if ctx.Err() != nil {
			return runtime.TextResult("CONTROL_REVOKED", false)
		}
		// Retain even an uncertain host result: Chromium may already hold the
		// selection. The broker's call-ID cache prevents a retry from selecting it twice.
		f.used += total
		f.directories = append(f.directories, dir)
		keep = true
		return host.Execute(ctx, request)
	}
	if args.Action != "get" || !membership.ValidateUUID(args.FileID) || !filepath.IsAbs(f.artifactDir) || !filepath.IsAbs(f.downloadRoot) {
		return runtime.TextResult("INVALID_REQUEST", false)
	}
	if existing := f.downloads[args.FileID]; existing != "" {
		return browserDownloaded(existing)
	}
	result := host.Execute(ctx, request)
	if !result.OK || ctx.Err() != nil {
		return result
	}
	var response struct {
		File localbrowser.StagedFile `json:"file"`
	}
	if json.Unmarshal([]byte(result.Text), &response) != nil || response.File.Path == "" {
		return runtime.TextResult("INVALID_RESULT", false)
	}
	// A completed file must belong to this Run, even when another Run's
	// download ID/path happens to be known by the model.
	runRoot := filepath.Join(f.downloadRoot, request.RunID+"-"+fmt.Sprint(request.Generation))
	file, err := inspectArtifact(response.File.Path, resolveRoots([]string{runRoot}), maxArtifactFileBytes)
	if err != nil || response.File.SHA256 != file.SHA256 || response.File.Size != int64(len(file.Content)) {
		return runtime.TextResult("FILE_REFUSED", false)
	}
	if f.used+int64(len(file.Content)) > browserRunFileBytes {
		return runtime.TextResult("LIMIT_EXCEEDED", false)
	}
	root, err := os.OpenRoot(f.artifactDir)
	if err != nil {
		return runtime.TextResult("UNAVAILABLE", false)
	}
	defer root.Close()
	destinationDir := filepath.Join("browser-downloads", args.FileID)
	if err := root.MkdirAll(destinationDir, 0700); err != nil {
		return runtime.TextResult("UNAVAILABLE", false)
	}
	destination := filepath.Join(destinationDir, file.Name)
	// Never overwrite files the Agent may already have modified.
	out, err := root.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return runtime.TextResult("FILE_CONFLICT", false)
	}
	_, writeErr := out.Write(file.Content)
	closeErr := out.Close()
	if writeErr != nil || closeErr != nil {
		_ = root.Remove(destination)
		return runtime.TextResult("UNAVAILABLE", false)
	}
	f.used += int64(len(file.Content))
	if f.downloads == nil {
		f.downloads = make(map[string]string)
	}
	f.downloads[args.FileID] = filepath.Join(f.artifactDir, destination)
	return browserDownloaded(f.downloads[args.FileID])
}

func browserDownloaded(path string) runtime.ToolResult {
	value, _ := json.Marshal(map[string]string{"path": path, "status": "downloaded"})
	return runtime.TextResult(string(value), true)
}

func (f *browserFiles) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for _, dir := range f.directories {
		_ = os.RemoveAll(dir)
	}
	f.directories = nil
}
