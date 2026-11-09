package supervisor

import (
	"encoding/base64"
	"strings"
	"testing"
)

const (
	skillOne = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	skillTwo = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func bundleOf(name string, files map[string]string) SkillBundle {
	bundle := SkillBundle{Name: name}
	for path, content := range files {
		bundle.Files = append(bundle.Files, SkillBundleFile{Path: path, ContentBase64: base64.StdEncoding.EncodeToString([]byte(content))})
	}
	return bundle
}

func TestSupervisorDeliversSkillsToAdapter(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	center.skillBundles = map[string]SkillBundle{
		skillOne: bundleOf("alpha", map[string]string{"SKILL.md": "---\nname: alpha\n---\nbody", "refs/a.txt": "A"}),
		skillTwo: bundleOf("beta", map[string]string{"SKILL.md": "x"}),
	}
	center.extend = func(rc *RunContext) {
		rc.Skills = []RunSkill{{ID: skillOne, Name: "alpha", Version: 1, FileCount: 2}, {ID: skillTwo, Name: "beta", Version: 1, FileCount: 1}}
	}
	newHarness(t, center, adapter, "")
	waitFor(t, "run started", func() bool { return adapter.startCount() == 1 })
	adapter.mu.Lock()
	request := adapter.requests[0]
	adapter.mu.Unlock()
	if len(request.Skills) != 2 || request.Skills[0].Name != "alpha" || len(request.Skills[0].Files) != 2 || request.Skills[1].Name != "beta" {
		t.Fatalf("skills = %+v", request.Skills)
	}
	for _, file := range request.Skills[0].Files {
		if file.Path == "SKILL.md" && !strings.Contains(string(file.Content), "body") {
			t.Fatalf("SKILL.md content = %q", file.Content)
		}
	}
}

func TestSupervisorWithoutSkillsLeavesRequestUnchanged(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	newHarness(t, center, adapter, "")
	waitFor(t, "run started", func() bool { return adapter.startCount() == 1 })
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.requests[0].Skills != nil || len(center.snapshot().skillCalls) != 0 {
		t.Fatalf("skills = %+v calls = %v", adapter.requests[0].Skills, center.snapshot().skillCalls)
	}
}

func TestSupervisorSkillDownloadFailureIsPrepareFailed(t *testing.T) {
	good := bundleOf("alpha", map[string]string{"SKILL.md": "x"})
	cases := map[string]func(*fakeCenter){
		"http error":     func(c *fakeCenter) { c.skillStatus = map[string]int{skillTwo: 409} },
		"missing bundle": func(c *fakeCenter) { delete(c.skillBundles, skillTwo) },
		"name mismatch":  func(c *fakeCenter) { c.skillBundles[skillTwo] = bundleOf("other", map[string]string{"SKILL.md": "x"}) },
		"bad path": func(c *fakeCenter) {
			c.skillBundles[skillTwo] = bundleOf("beta", map[string]string{"SKILL.md": "x", "../evil": "y"})
		},
		"no SKILL.md": func(c *fakeCenter) { c.skillBundles[skillTwo] = bundleOf("beta", map[string]string{"a.md": "x"}) },
		"bad base64": func(c *fakeCenter) {
			c.skillBundles[skillTwo] = SkillBundle{Name: "beta", Files: []SkillBundleFile{{Path: "SKILL.md", ContentBase64: "!!!"}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			center, adapter := newCenter(), &fakeAdapter{}
			center.skillBundles = map[string]SkillBundle{skillOne: good, skillTwo: bundleOf("beta", map[string]string{"SKILL.md": "x"})}
			mutate(center)
			center.extend = func(rc *RunContext) {
				rc.Skills = []RunSkill{{ID: skillOne, Name: "alpha"}, {ID: skillTwo, Name: "beta"}}
			}
			newHarness(t, center, adapter, "")
			waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
			snapshot := center.snapshot()
			if snapshot.prepareFailed[0] != "PREPARE_FAILED" || snapshot.starts != 0 || adapter.startCount() != 0 {
				t.Fatalf("prepare-failed=%v starts=%d processes=%d", snapshot.prepareFailed, snapshot.starts, adapter.startCount())
			}
		})
	}
}

func TestSupervisorRejectsInvalidSkillManifest(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	center.extend = func(rc *RunContext) { rc.Skills = []RunSkill{{ID: skillOne, Name: "../etc"}} }
	newHarness(t, center, adapter, "")
	waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
	if len(center.snapshot().skillCalls) != 0 || adapter.startCount() != 0 {
		t.Fatalf("calls=%v processes=%d", center.snapshot().skillCalls, adapter.startCount())
	}
}
