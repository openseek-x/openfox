package artifactreview

import (
	"openfox/internal/office"
	"os"
	"testing"
)

func TestReviewMultipleRegionsDoNotUseTextOffsets(t *testing.T) {
	snapshots := []Snapshot{
		{Anchor: Anchor{Kind: "region", Page: 1, X: .1, Y: .1, Width: .1, Height: .1}},
		{Anchor: Anchor{Kind: "region", Page: 1, X: .6, Y: .6, Width: .1, Height: .1}},
	}
	if err := ValidateSelection(snapshots); err != nil {
		t.Fatalf("独立区域被按相同文字偏移拒绝：%v", err)
	}
}

func TestReviewApplySubsetOfComments(t *testing.T) {
	source := "abcdef"
	snapshots := []Snapshot{{ThreadID: "edit", Anchor: Anchor{Kind: "text", Start: 0, End: 1, Quote: "a"}}, {ThreadID: "question", Anchor: Anchor{Kind: "text", Start: 2, End: 3, Quote: "c"}}}
	got, err := Apply(source, "text/plain", snapshots, Submission{SHA256: Digest(source), Edits: []Edit{{ThreadID: "edit", Expected: "a", Replacement: "A"}}})
	if err != nil || got != "Abcdef" {
		t.Fatalf("不能只应用要求修改的一条批注：%q %v", got, err)
	}
}

func TestReviewApplyOfficeSubset(t *testing.T) {
	raw, err := os.ReadFile("../office/testdata/sample.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := office.Read(raw, office.XLSX)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := []Snapshot{}
	for _, unit := range pkg.View().Units {
		if unit.Editable && unit.Type == "text" {
			snapshots = append(snapshots, Snapshot{ThreadID: unit.ID, Anchor: Anchor{Kind: "office", Target: unit.ID, Quote: unit.Text}})
		}
	}
	if len(snapshots) < 2 {
		t.Fatal("测试文件缺少可编辑文本单元")
	}
	first := snapshots[0]
	got, err := Apply(string(raw), office.XLSX, snapshots, Submission{SHA256: Digest(string(raw)), Edits: []Edit{{ThreadID: first.ThreadID, Expected: first.Anchor.Quote, Replacement: "新值"}}})
	if err != nil {
		t.Fatalf("Office 不能只修改批注子集：%v", err)
	}
	updated, err := office.Read([]byte(got), office.XLSX)
	if err != nil {
		t.Fatal(err)
	}
	before, after := pkg.View().Units, updated.View().Units
	if len(before) != len(after) {
		t.Fatal("修改改变了单元数")
	}
	for i, unit := range before {
		want := unit.Text
		if unit.ID == first.Anchor.Target {
			want = "新值"
		}
		if after[i].Text != want {
			t.Fatalf("未选单元被修改：%s", unit.Label)
		}
	}
}
