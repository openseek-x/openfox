package agents

import (
	"encoding/json"
	"testing"
)

func TestVariantRuntimeAndScheduleInheritanceAreIndependent(t *testing.T) {
	parent := ConfigInput{Runtime: "codex", Model: "default", RunSettings: RunSettings{ModelSource: "MACHINE", WorkingHours: json.RawMessage(`{"timezone":"UTC"}`)}}
	child := ConfigInput{Runtime: "codex", Model: "explicit-model", RunSettings: RunSettings{ModelSource: "EXPLICIT"}}
	runtimeOnly := mergeConfig(parent, child, map[string]bool{GroupRuntime: true})
	if runtimeOnly.ModelSource != "MACHINE" || runtimeOnly.Model != "default" || runtimeOnly.WorkingHours != nil {
		t.Fatalf("runtime-only inheritance = %+v", runtimeOnly)
	}
	settingsOnly := mergeConfig(parent, child, map[string]bool{GroupRunSettings: true})
	if settingsOnly.ModelSource != "EXPLICIT" || settingsOnly.Model != "explicit-model" || string(settingsOnly.WorkingHours) != string(parent.WorkingHours) {
		t.Fatalf("settings-only inheritance = %+v", settingsOnly)
	}
	parent.WorkingHours = nil
	cleared := mergeConfig(parent, settingsOnly, map[string]bool{GroupRunSettings: true})
	if string(cleared.WorkingHours) != "null" {
		t.Fatal("restoring disabled parent schedule must explicitly clear child's previous schedule")
	}
}

func TestVariantModelSourceChangesOnlyRuntimeGroup(t *testing.T) {
	before := ConfigInput{Runtime: "codex", Model: "default", RunSettings: RunSettings{ModelSource: "EXPLICIT"}}
	after := before
	after.ModelSource = "MACHINE"
	groups := changedConfigGroups(before, after)
	if len(groups) != 1 || !groups[GroupRuntime] {
		t.Fatalf("changed groups = %+v", groups)
	}
	overridden := applyOverrides(after, VariantOverrides{Runtime: &VariantRuntime{Runtime: "codex", Model: "specific"}})
	if overridden.ModelSource != "EXPLICIT" {
		t.Fatal("explicit model override retained machine inheritance")
	}
}

func TestVariantOmittedScheduleAndModelSourcePreserveInheritance(t *testing.T) {
	before := ConfigInput{Runtime: "codex", Model: "default", RunSettings: RunSettings{ModelSource: "MACHINE", WorkingHours: json.RawMessage(`{"timezone":"UTC"}`)}}
	submitted := before
	submitted.ModelSource = ""
	submitted.WorkingHours = nil
	effective := resolveOmittedVariantSettings(before, submitted)
	if len(changedConfigGroups(before, effective)) != 0 {
		t.Fatal("omitted settings broke inherited groups")
	}
	submitted.Model = "other"
	if resolveOmittedVariantSettings(before, submitted).ModelSource != "EXPLICIT" {
		t.Fatal("new explicit model retained machine source")
	}
}
