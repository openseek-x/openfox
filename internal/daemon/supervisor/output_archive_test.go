package supervisor

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"openfox/internal/runtime"
)

func TestOutputArchivePrivateBoundedRedactedAndCleaned(t *testing.T) {
	a := &outputArchive{root: t.TempDir(), redact: func(s string) string { return strings.ReplaceAll(s, "fixture-secret", "[REDACTED]") }}
	defer a.close()
	a.record("exec", "tool-1", "HEAD fixture-secret\n"+strings.Repeat("中文🙂", outputArchiveLimit/10)+"TAIL")
	a.record("exec", "tool-2", "NEXT_UNCAPTURED_MARKER")
	content, err := a.content()
	if err != nil || len(content) > outputArchiveLimit || !utf8.Valid(content) || !a.capped || !a.required {
		t.Fatalf("archive invalid: bytes=%d err=%v", len(content), err)
	}
	text := string(content)
	if strings.Contains(text, "fixture-secret") || strings.Contains(text, "NEXT_UNCAPTURED_MARKER") || !strings.Contains(text, "[REDACTED]") || !strings.HasSuffix(text, archiveFooter) {
		t.Fatal("archive content invalid")
	}
	info, err := a.file.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("archive is not private")
	}
	dir := a.dir
	a.close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("archive was not cleaned")
	}
}

func TestOutputArchiveSmallReplyAndDiskFailure(t *testing.T) {
	a := &outputArchive{root: t.TempDir()}
	defer a.close()
	a.record("reply", "", "normal reply")
	if a.required {
		t.Fatal("small reply should not create a visible artifact")
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	b := &outputArchive{root: blocked}
	defer b.close()
	b.record("exec", "large", "text")
	if _, err := b.content(); err == nil || !b.required {
		t.Fatal("disk failure hidden")
	}
}

func TestSupervisorOutputArchiveDelivery(t *testing.T) {
	for _, status := range []int{0, http.StatusRequestEntityTooLarge, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			center, adapter := newCenter(), &fakeAdapter{}
			center.artifactStatus = status
			full := "HEAD\n" + strings.Repeat("result\n", 5000) + "TAIL"
			adapter.onStart = func(req runtime.RunRequest) { req.RecordOutput("exec", "tool-1", full) }
			newHarness(t, center, adapter, "")
			waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
			snap := center.snapshot()
			if snap.results[0].Status != "SUCCEEDED" || snap.results[0].Output != "OK" {
				t.Fatal("archive changed execution result")
			}
			if status == 0 {
				if len(snap.results[0].ArtifactIDs) != 1 || len(snap.artifactUploads) != 1 || !strings.Contains(string(snap.artifactUploads[0].Body), full) {
					t.Fatal("archive not delivered with result")
				}
			} else {
				if len(snap.results[0].ArtifactIDs) != 0 {
					t.Fatal("failed upload advertised as available")
				}
				found := false
				for _, event := range snap.accepted {
					if event.Type == "warning" && strings.Contains(string(event.Payload), "未能上传") {
						found = true
					}
				}
				if !found {
					t.Fatal("archive delivery failure was not visible")
				}
			}
		})
	}
}

func TestResultPreviewClippingIsVisible(t *testing.T) {
	input := resultInput(runtime.Result{Status: runtime.StatusSucceeded, Output: strings.Repeat("中🙂", 50000)})
	if input.Status != "SUCCEEDED" || len(input.Output) > maxResultOutput || !utf8.ValidString(input.Output) || !strings.Contains(input.Output, runtime.OutputTruncationNotice) {
		t.Fatal("silent or invalid result clipping")
	}
}
