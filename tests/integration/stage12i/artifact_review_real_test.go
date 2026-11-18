package stage12i

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openfox/internal/artifactreview"
	"openfox/internal/artifacts"
	"openfox/internal/daemon"
	"openfox/internal/docs"
	"openfox/internal/office"
	"openfox/internal/runtime/codex"
)

// Explicitly opt in: this calls a real model using the installed Codex login.
// Run with OFD_REAL_CODEX=1 OFD_REAL_CODEX_VERSION=<installed version>
// OPENFOX_TEST_FLAGS='-run TestRealCodexArtifactReview -v' bash tests/integration/stage12i/run.sh.
// The script owns the temporary database. All application state is temporary;
// only the credential source is real (the adapter handles refresh with CAS).
func realReviewStack(t *testing.T) (*stack, device, string, string, string) {
	if os.Getenv("OFD_REAL_CODEX") != "1" {
		t.Skip("set OFD_REAL_CODEX=1 to call the installed Codex")
	}
	version := os.Getenv("OFD_REAL_CODEX_VERSION")
	if version == "" {
		t.Fatal("OFD_REAL_CODEX_VERSION is required")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	credentialHome := os.Getenv("CODEX_HOME")
	if credentialHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		credentialHome = filepath.Join(home, ".codex")
	}
	s := newStack(t, "", 15*time.Minute)
	s.seed()
	s.startServer()
	browserOrigin := ""
	if os.Getenv("OFD_REAL_REVIEW_BROWSER") == "1" {
		browserOrigin = s.startReviewBrowserWeb()
	}
	d := s.registerDevice("real-review")
	config, err := daemon.LoadDeviceConfig(d.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeExecutable, config.SupportedVersions = executable, []string{version}
	if err := daemon.WriteDeviceConfig(d.ConfigPath, config); err != nil {
		t.Fatal(err)
	}
	startProc(t, "real review daemon", filepath.Join(d.StateDir, "daemon.log"),
		s.childEnv("CODEX_HOME="+credentialHome), s.cliBin,
		"daemon", "run", "--config", d.ConfigPath, "--heartbeat-interval", "500ms", "--offer-poll-interval", "1s")
	s.waitOnline(d)
	var deviceView struct {
		Capabilities struct {
			Review   bool `json:"file_comments"`
			Runtimes map[string]struct {
				Runnable bool          `json:"runnable"`
				Models   []codex.Model `json:"models"`
			} `json:"runtimes"`
		} `json:"capabilities"`
	}
	s.owner().must(http.StatusOK, http.MethodGet, "devices/"+d.ID, nil, &deviceView)
	runtime := deviceView.Capabilities.Runtimes["codex"]
	if !deviceView.Capabilities.Review || !runtime.Runnable || len(runtime.Models) == 0 {
		t.Fatal("real Codex or artifact review capability unavailable")
	}
	model := runtime.Models[0].ID
	for _, candidate := range runtime.Models {
		if candidate.IsDefault {
			model = candidate.ID
		}
	}
	grant := s.text(`SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, s.org, d.ID)
	var created struct{ ID string }
	s.owner().must(http.StatusCreated, http.MethodPost, "spaces", map[string]any{"name": "Real review acceptance", "visibility": "SHARED"}, &created)
	space := created.ID
	s.owner().must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/agents", map[string]any{
		"name": "Review acceptance", "kind": "CODE", "visibility": "PRIVATE",
		"config": map[string]any{"computer_id": d.ID, "grant_id": grant, "runtime": "codex", "model": model, "run_timeout_seconds": 240, "approval_mode": "WORKSPACE_WRITE"},
	}, &created)
	agent := created.ID
	t.Logf("real Codex version=%s model=%s", version, model)

	return s, d, space, agent, browserOrigin
}

func TestRealCodexArtifactReview(t *testing.T) {
	s, d, space, agent, browserOrigin := realReviewStack(t)
	grant := s.text(`SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, s.org, d.ID)
	model := s.text(`SELECT model FROM openfox.agent_config_versions WHERE organization_id=$1::uuid AND agent_id=$2::uuid AND config_version=1`, s.org, agent)
	kinds := []string{"native", "csv", "html", "docx", "xlsx", "pptx"}
	if selected := os.Getenv("OFD_REAL_REVIEW_KINDS"); selected != "" {
		kinds = strings.Split(selected, ",")
	}

	for _, kind := range kinds {
		conversation := s.createConversation(space, agent)
		var node docs.NodeDetail
		var run, artifact, name, mime string
		source, quote, replacement := "# 验收🙂\r\n\r\n原文\r\n\r\n末尾保持。\r\n", "原文", "新文"
		anchor := artifactreview.Anchor{Kind: "text"}
		if kind == "native" {
			if browserOrigin != "" {
				source = "# 验收🙂\n\n原文\n\n末尾保持。"
			}
			s.owner().must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/docs", map[string]any{"container": map[string]string{"kind": "personal"}, "kind": "doc", "title": "Acceptance", "body": source}, &node)
		} else {

			source, quote, replacement = "name,note\r\n中文🙂,\"旧,值\"\r\n末行,保持\r\n", "\"旧,值\"", "新,值"
			anchor.Kind, anchor.Row, anchor.Column = "cell", 1, 1
			name, mime = "acceptance.csv", "text/csv"
			if kind == "html" {
				source = "\ufeff<!doctype html><html><head><style>h1{color:blue}</style></head><body>前🙂\r\n<h1>原文</h1>\r\n<p>保持 &amp; 不变</p></body></html>"
				quote, replacement = "<h1>原文</h1>", "<h1>新文</h1>"
				anchor = artifactreview.Anchor{Kind: "text"}
				name, mime = "acceptance.html", "text/plain"
			}
			if officeMIME := office.MIMEForName("acceptance." + kind); officeMIME != "" {
				raw, e := os.ReadFile("../../../internal/office/testdata/sample." + kind)
				if e != nil {
					t.Fatal(e)
				}
				source, name, mime = string(raw), "acceptance."+kind, officeMIME
				pkg, e := office.Read(raw, mime)
				if e != nil {
					t.Fatal(e)
				}
				unit := pkg.View().Units[0]
				quote, replacement = unit.Text, "Updated & <text>"
				anchor = artifactreview.Anchor{Kind: "office", Target: unit.ID, Quote: quote}
			}
			// Seed only the existing source artifact. Revision dispatch and every
			// subsequent operation must use the real public API/Daemon/model.
			run, artifact = newID(t), newID(t)
			key := s.org + "/" + run + "/" + artifact
			stored, err := (artifacts.Local{Root: s.storage}).Put(s.ctx, key, strings.NewReader(source), office.MaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			s.exec(`INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,generation,finished_at,runtime,model,approval_mode,reasoning_effort)
				VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,1,1,$8::uuid,'SUCCEEDED',1,clock_timestamp(),'codex',$9,'READ_ONLY','DEFAULT')`, s.org, run, space, conversation, agent, d.ID, grant, drillOwnerID, model)
			s.exec(`INSERT INTO openfox.artifacts(organization_id,id,run_id,name,storage_key,size_bytes,sha256,mime_type,retention_until)
				VALUES($1::uuid,$2::uuid,$3::uuid,$7,$4,$5,$6,$8,clock_timestamp()+interval '1 day')`, s.org, artifact, run, key, stored.Size, stored.SHA256, name, mime)
			if office.MIME(mime) {
				route := "runs/" + run + "/artifacts/" + artifact + "/office"
				var projection struct {
					office.View
					SHA256 string `json:"sha256"`
				}
				s.owner().must(http.StatusOK, http.MethodGet, route, nil, &projection)
				if projection.SHA256 != stored.SHA256 || projection.Units[0].Text != quote {
					t.Fatal("incorrect Office projection")
				}
				for _, user := range []string{drillMemberID, drillOutsiderID} {
					status, _ := s.login(user).do(http.MethodGet, route, nil)
					if status != 403 && status != 404 {
						t.Fatalf("Office projection leaked to %s: %d", user, status)
					}
				}
			}
		}
		if anchor.Kind != "office" {
			anchor.Start, anchor.End, anchor.Quote = strings.Index(source, quote), strings.Index(source, quote)+len(quote), quote
		}
		input := artifactreview.Input{ID: newID(t), ArtifactID: artifact, SHA256: artifactreview.Digest(source), Comments: []artifactreview.Snapshot{{ThreadID: newID(t), Anchor: anchor, Body: "请把选中内容精确替换成「" + replacement + "」，其余内容保持不变。"}}}
		if kind == "native" {
			input.SpaceID, input.NodeID, input.NodeVersion = space, node.ID, int64(node.Version)
		}
		if browserOrigin != "" {
			s.browserReview(browserOrigin, kind, conversation, space, node.ID, artifact, "", quote, replacement)
		} else {
			// Ordinary messages trigger the existing dispatcher, with normal permissions.
			s.owner().must(http.StatusCreated, http.MethodPost, "conversations/"+conversation+"/messages", map[string]any{"body": "请按附带的文件批注直接修改文件。先读取上下文，然后使用文件批注工具应用修改。", "client_message_id": newID(t), "mentions": []map[string]string{{"agent_id": agent}}, "file_contexts": []artifactreview.Input{input}}, nil)
		}
		var completed runRow
		s.waitFor(kind+" ordinary chat completion", 5*time.Minute, func() bool {
			completed = s.latestRun(conversation)
			return completed.ID != "" && completed.ID != run && (completed.State == "SUCCEEDED" || completed.State == "FAILED" || completed.State == "CANCELED")
		})
		if completed.State != "SUCCEEDED" {
			t.Fatalf("%s failed: %+v\n%s", kind, completed, s.diagnostics())
		}
		s.expect("WORKSPACE_WRITE", `SELECT approval_mode FROM openfox.runs WHERE id=$1::uuid`, completed.ID)
		for _, tool := range []string{"openfox_file_comment_read", "openfox_file_comment_apply"} {
			s.expect("true", `SELECT EXISTS(SELECT 1 FROM openfox.run_events WHERE organization_id=$1::uuid AND run_id=$2::uuid AND safe_payload #>> '{data,step,tool_name}'=$3 AND safe_payload #>> '{data,step,state}'='done')::text`, s.org, completed.ID, tool)
		}
		if browserOrigin == "" {
			s.acceptRunProposal(conversation, completed.ID)
		} else {
			s.expect("1", `SELECT count(*)::text FROM openfox.workspace_proposals WHERE run_id=$1::uuid AND status='ACCEPTED'`, completed.ID)
		}
		resultID := s.text(`SELECT COALESCE(f.result_artifact_id::text,'') FROM openfox.message_file_contexts f JOIN openfox.conversation_inputs i ON i.organization_id=f.organization_id AND i.message_id=f.message_id WHERE f.organization_id=$1::uuid AND i.run_id=$2::uuid`, s.org, completed.ID)
		want := strings.Replace(source, quote, replacement, 1)
		if kind == "native" && browserOrigin != "" {
			want += "（画布编辑）"
		}
		if kind == "csv" {
			want = strings.Replace(source, "旧,值", replacement, 1)
		}
		var got string
		if kind == "native" {
			var fresh docs.NodeDetail
			s.owner().must(http.StatusOK, http.MethodGet, "spaces/"+space+"/docs/"+node.ID, nil, &fresh)
			expectedVersion := node.Version + 1
			if browserOrigin != "" {
				expectedVersion++
			}
			if fresh.Body == nil || fresh.Version != expectedVersion {
				t.Fatal("native document was not directly updated")
			}
			got = *fresh.Body
		} else {
			if resultID == "" {
				t.Fatal("missing directly published file")
			}
			status, bytes := s.owner().do(http.MethodGet, "runs/"+completed.ID+"/artifacts/"+resultID, nil)
			if status != http.StatusOK {
				t.Fatalf("output download %d", status)
			}
			got = string(bytes)
			status, original := s.owner().do(http.MethodGet, "runs/"+run+"/artifacts/"+artifact, nil)
			if status != 200 || string(original) != source {
				t.Fatal("source storage corrupted")
			}
			s.expect("true", `SELECT EXISTS(SELECT 1 FROM openfox.workspace_proposals WHERE result_artifact_id=$1::uuid AND status='ACCEPTED')::text`, resultID)
		}
		if office.MIME(mime) {
			before, e := office.Read([]byte(source), mime)
			if e != nil {
				t.Fatal(e)
			}
			after, e := office.Read([]byte(got), mime)
			if e != nil {
				t.Fatal(e)
			}
			if len(before.View().Units) != len(after.View().Units) {
				t.Fatal("Office structure changed")
			}
			for i, u := range before.View().Units {
				expected := u.Text
				if u.ID == anchor.Target {
					expected = replacement
				}
				if after.View().Units[i].Text != expected {
					t.Fatalf("unexpected edit %s", u.Label)
				}
			}
		} else if got != want {
			t.Fatalf("%s result %q want %q", kind, got, want)
		}
		t.Logf("%s: ordinary message → real Codex read/apply → explicit proposal acceptance → updated preview", kind)
	}
}
