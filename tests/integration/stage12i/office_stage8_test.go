package stage12i

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"openfox/internal/artifactreview"
	"openfox/internal/artifacts"
	"openfox/internal/office"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Five real model runs, all through the production browser build and temporary
// API/database/daemon. The private PPT is read-only input, never committed.
func TestRealOfficeStage8(t *testing.T) {
	if os.Getenv("OFD_REAL_OFFICE_STAGE8") != "1" {
		t.Skip("explicit Office/model acceptance only")
	}
	s, d, space, agent, origin := realReviewStack(t)
	if origin == "" {
		t.Fatal("OFD_REAL_REVIEW_BROWSER=1 is required")
	}
	grant := s.text(`SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, s.org, d.ID)
	model := s.text(`SELECT model FROM openfox.agent_config_versions WHERE organization_id=$1::uuid AND agent_id=$2::uuid AND config_version=1`, s.org, agent)
	kinds := []string{"docx", "xlsx", "pptx", "xlsx_formula", "pptx_chart"}
	if selected := os.Getenv("OFD_REAL_REVIEW_KINDS"); selected != "" {
		kinds = strings.Split(selected, ",")
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			ext := strings.Split(kind, "_")[0]
			mime := office.MIMEForName("file." + ext)
			file := filepath.Join("../../../tests/frontend/e2e/fixtures/office-preview", "complex."+ext)
			if kind == "pptx" {
				file = os.Getenv("OPENFOX_TEST_PPT_FIXTURE")
				if file == "" {
					t.Fatal("private PPT fixture required")
				}
			}
			if kind == "pptx_chart" {
				file = "../../../tests/frontend/e2e/fixtures/office-preview/styled.pptx"
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			p, err := office.Read(raw, mime)
			if err != nil {
				t.Fatal(err)
			}
			var unit office.Unit
			for _, u := range p.View().Units {
				if (ext == "docx" && u.Text == "重复文本") || (ext == "xlsx" && u.Label == "B3") || (kind == "pptx" && u.Text == "黄仁勋最近的风波") {
					unit = u
					break
				}
			}
			replacement := "更新文本"
			if ext == "xlsx" {
				replacement = "11"
			}
			if kind == "pptx" {
				replacement = "黄仁勋近期风波"
			}
			anchor := artifactreview.Anchor{Kind: "office", Target: unit.ID, Quote: unit.Text}
			instruction := "请把选中文字精确改成「" + replacement + "」，保留原有样式和所有其他内容。先读取文件批注，再用 file_comment_apply 交付更新文件。"
			complex := strings.Contains(kind, "_")
			changedPart := ""
			if kind == "xlsx_formula" {
				quote, _ := p.RangeQuote("xl/worksheets/sheet1.xml", "D3")
				anchor = artifactreview.Anchor{Kind: "office_range", Target: "xl/worksheets/sheet1.xml", Range: "D3", Quote: quote}
				replacement = "=SUM(C3)/B3"
				changedPart = "xl/worksheets/sheet1.xml"
				instruction = "把 D3 的公式改成 =SUM(C3)/B3，保留其他单元格、原图片、图表、冻结、合并、数字格式和工作表。先读取批注并导出原文件，修改单独副本，使用 file_comment_replace 交付；不要通过精确文字补丁伪造公式，不要重新生成工作簿。"
			} else if kind == "pptx_chart" {
				anchor = artifactreview.Anchor{Kind: "region", Page: 1, X: .5, Y: .2, Width: .45, Height: .6}
				replacement = "E5484D"
				changedPart = "ppt/charts/chart1.xml"
				instruction = "只把这张幻灯片柱状图的柱子改成红色 #E5484D，保留全部数值、分类、文字、原始图表和嵌入工作簿。先读取批注并导出原文件，在独立副本中修改图表系列填充色，使用 file_comment_replace 交付；不要重新生成 PPT 或把图表栅格化。"
			} else if unit.ID == "" {
				t.Fatal("expected source unit missing")
			}
			conversation := s.createConversation(space, agent)
			run, artifact := newID(t), newID(t)
			key := s.org + "/" + run + "/" + artifact
			stored, err := (artifacts.Local{Root: s.storage}).Put(s.ctx, key, bytes.NewReader(raw), office.MaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			s.exec(`INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,generation,finished_at,runtime,model,approval_mode,reasoning_effort) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,1,1,$8::uuid,'SUCCEEDED',1,clock_timestamp(),'codex',$9,'READ_ONLY','DEFAULT')`, s.org, run, space, conversation, agent, d.ID, grant, drillOwnerID, model)
			s.exec(`INSERT INTO openfox.artifacts(organization_id,id,run_id,name,storage_key,size_bytes,sha256,mime_type,retention_until) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,clock_timestamp()+interval '1 day')`, s.org, artifact, run, "office-"+kind+"."+ext, key, stored.Size, stored.SHA256, mime)
			s.browserReview(origin, kind, conversation, space, "", artifact, "", anchor.Quote, replacement, map[string]any{"stage8": true, "source_run": run, "anchor": anchor, "instruction": instruction, "extension": ext})
			var completed runRow
			s.waitFor("Office model completion", 5*time.Minute, func() bool {
				completed = s.latestRun(conversation)
				return completed.ID != "" && completed.ID != run && (completed.State == "SUCCEEDED" || completed.State == "FAILED" || completed.State == "CANCELED")
			})
			if completed.State != "SUCCEEDED" {
				t.Fatalf("model did not complete: %s", completed.State)
			}
			tools := []string{"openfox_file_comment_read", "openfox_file_comment_apply"}
			if complex {
				tools = []string{"openfox_file_comment_read", "openfox_file_comment_export", "openfox_file_comment_replace"}
			}
			for _, tool := range tools {
				s.expect("true", `SELECT EXISTS(SELECT 1 FROM openfox.run_events WHERE organization_id=$1::uuid AND run_id=$2::uuid AND safe_payload #>> '{data,step,tool_name}'=$3 AND safe_payload #>> '{data,step,state}'='done')::text`, s.org, completed.ID, tool)
			}
			s.expect("1", `SELECT count(*)::text FROM openfox.workspace_proposals WHERE run_id=$1::uuid AND status='ACCEPTED'`, completed.ID)
			resultID := s.text(`SELECT COALESCE(f.result_artifact_id::text,'') FROM openfox.message_file_contexts f JOIN openfox.conversation_inputs i ON i.organization_id=f.organization_id AND i.message_id=f.message_id WHERE f.organization_id=$1::uuid AND i.run_id=$2::uuid`, s.org, completed.ID)
			status, out := s.owner().do(http.MethodGet, "runs/"+completed.ID+"/artifacts/"+resultID, nil)
			if status != 200 {
				t.Fatalf("download %d", status)
			}
			status, original := s.owner().do(http.MethodGet, "runs/"+run+"/artifacts/"+artifact, nil)
			if status != 200 || !bytes.Equal(raw, original) {
				t.Fatal("original mutated")
			}
			after, err := office.Read(out, mime)
			if err != nil {
				t.Fatal(err)
			}
			if len(after.View().Units) != len(p.View().Units) {
				t.Fatal("unexpected structure change")
			}
			for i, u := range p.View().Units {
				expected := u.Text
				if !complex && u.ID == unit.ID {
					expected = replacement
				}
				if kind == "xlsx_formula" && u.Label == "D3" && u.CellSource.Sheet == 0 {
					expected = replacement
				}
				if after.View().Units[i].Text != expected {
					t.Fatalf("unexpected cell/text change: %s", u.Label)
				}
			}
			beforeParts, afterParts := officeParts(t, raw), officeParts(t, out)
			if complex {
				if len(beforeParts) != len(afterParts) {
					t.Fatal("ZIP parts changed")
				}
				for name, body := range beforeParts {
					if name != changedPart && !bytes.Equal(body, afterParts[name]) {
						t.Fatalf("unrelated part changed: %s", name)
					}
				}
				if kind == "pptx_chart" && !bytes.Contains(afterParts[changedPart], []byte("E5484D")) {
					t.Fatal("chart fill missing")
				}
			}
			if evidence := os.Getenv("OPENFOX_REVIEW_BROWSER_EVIDENCE"); evidence != "" {
				_ = os.MkdirAll(evidence, 0700)
				_ = os.WriteFile(filepath.Join(evidence, "updated-"+kind+"."+ext), out, 0600)
				data, _ := json.MarshalIndent(map[string]any{"kind": kind, "source_sha256": stored.SHA256, "result_sha256": artifactreview.Digest(string(out)), "original_unchanged": true, "native_source_units": len(after.View().Units), "complex": complex, "tools": tools}, "", "  ")
				_ = os.WriteFile(filepath.Join(evidence, "result-"+kind+".json"), append(data, '\n'), 0600)
			}
			t.Logf("%s: original SHA checked, real UI/model tools, new artifact rendered, original/unrelated content preserved", kind)
		})
	}
}
func officeParts(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		t.Fatal(e)
	}
	out := map[string][]byte{}
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		_ = r.Close()
		if e != nil {
			t.Fatal(fmt.Errorf("read %s: %w", f.Name, e))
		}
		out[f.Name] = b
	}
	return out
}
