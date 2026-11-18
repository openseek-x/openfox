package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxLogPageBytes = 32 * 1024
const maxLogPageLines = 200
const hiddenLogLine = "[REDACTED: unstructured log line]"

type ServiceLogPage struct {
	Lines      []string `json:"lines"`
	NextCursor *string  `json:"next_cursor"`
	Reset      bool     `json:"reset"`
}

type serviceLogCursor struct {
	File   string `json:"f"`
	Before int64  `json:"b"`
}

// Safe event names are code-owned. Free-form errors, token values, user paths,
// identifiers and legacy lines are never returned to the hosted renderer.
var safeServiceEvents = map[string]bool{
	"commands_claim_failed": true, "commands_execute_failed": true, "commands_executed": true,
	"commands_list_failed": true, "commands_result_failed": true,
	"daemon_token_renew_failed": true, "daemon_token_renewed": true,
	"runtime_session_exports_expired": true, "storage_policy_check_failed": true,
	"supervisor_abandoned_runs": true, "supervisor_artifact_dir_failed": true,
	"supervisor_artifact_published": true, "supervisor_artifact_upload_failed": true,
	"supervisor_attachment_download_failed": true, "supervisor_claim_failed": true,
	"supervisor_cleanup_outside_root": true, "supervisor_cleanup_worktree_failed": true,
	"supervisor_direct_repository_busy": true, "supervisor_directory_fact_unavailable": true,
	"supervisor_events_rejected": true, "supervisor_events_spill_failed": true,
	"supervisor_events_upload_failed": true, "supervisor_journal_failed": true,
	"supervisor_lease_expired_locally": true, "supervisor_lease_rejected": true,
	"supervisor_list_offers_failed": true, "supervisor_outbox_events_rejected": true,
	"supervisor_outbox_list_failed": true, "supervisor_prepare_failed_report_failed": true,
	"supervisor_result_artifacts_rejected": true, "supervisor_result_upload_failed": true,
	"supervisor_result_usage_rejected": true, "supervisor_run_stopped_lease_lost": true,
	"supervisor_runtime_start_failed": true, "supervisor_runtime_stop_failed": true,
	"supervisor_runtime_wait_failed": true, "supervisor_skill_download_failed": true,
	"supervisor_task_tool_failed": true, "supervisor_workspace_record_failed": true,
}

var serviceLogLine = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}) (DEBUG|INFO|WARN|ERROR) ([a-z_]+)(?: .*)?$`)

func redactServiceLogLine(line string) string {
	match := serviceLogLine.FindStringSubmatch(line)
	if match == nil || !safeServiceEvents[match[3]] {
		return hiddenLogLine
	}
	return match[1] + " " + match[2] + " " + match[3] + " [details redacted]"
}

// ServiceLogs reads only the manager's fixed log, newest page first, without
// loading the whole file. A cursor contains file identity and a byte boundary,
// never a path. Replacing or truncating a log resets to its newest page.
func (m Manager) ServiceLogs(cursor string) (ServiceLogPage, error) {
	if m.registrationConfigPath != "" {
		var page ServiceLogPage
		err := m.withServiceLock(func() error {
			var err error
			page, err = m.serviceLogs(cursor)
			return err
		})
		return page, err
	}
	return m.serviceLogs(cursor)
}

func (m Manager) serviceLogs(cursor string) (ServiceLogPage, error) {
	page := ServiceLogPage{Lines: []string{}}
	if !m.supportsService() {
		return page, ErrServiceUnavailable
	}
	var before serviceLogCursor
	if cursor != "" {
		if len(cursor) > 256 {
			return page, errors.New("invalid log cursor")
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &before) != nil || before.Before < 0 || before.File == "" {
			return page, errors.New("invalid log cursor")
		}
	}
	if info, err := os.Lstat(filepath.Dir(m.LogPath)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return page, nil
		}
		return page, ErrServiceUnavailable
	} else if !info.IsDir() {
		return page, ErrUnsafeFile
	}
	file, err := openServiceFile(m.LogPath, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return page, nil
	}
	if err != nil {
		return page, ErrServiceUnavailable
	}
	defer file.Close()
	identity, err := serviceFileIdentity(file)
	if err != nil {
		return page, ErrServiceUnavailable
	}
	info, err := file.Stat()
	if err != nil {
		return page, ErrServiceUnavailable
	}
	end := info.Size()
	if cursor != "" {
		if identity != before.File || before.Before > end {
			page.Reset = true
		} else {
			end = before.Before
		}
	}
	start := max(int64(0), end-maxLogPageBytes)
	data := make([]byte, end-start)
	n, err := file.ReadAt(data, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return page, ErrServiceUnavailable
	}
	if int64(n) != end-start {
		page.Reset = true
		return page, nil
	}
	data = data[:n]
	var previous [1]byte
	if start > 0 {
		_, _ = file.ReadAt(previous[:], start-1)
	}
	if start > 0 && previous[0] != '\n' {
		// Drop the first partial line so a token split at a page boundary cannot
		// be mistaken for a standalone safe message.
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			start += int64(newline + 1)
			data = data[newline+1:]
		} else {
			page.Lines = []string{hiddenLogLine}
			data = nil
		}
	}
	if len(data) > 0 {
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if len(lines) > maxLogPageLines {
			for _, line := range lines[:len(lines)-maxLogPageLines] {
				start += int64(len(line) + 1)
			}
			lines = lines[len(lines)-maxLogPageLines:]
		}
		for _, line := range lines {
			page.Lines = append(page.Lines, redactServiceLogLine(line))
		}
	}
	if start > 0 {
		raw, _ := json.Marshal(serviceLogCursor{File: identity, Before: start})
		next := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &next
	}
	return page, nil
}
