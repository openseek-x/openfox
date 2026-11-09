package stage2c

// 技能端到端：真实中心 + 真实 openfox daemon（假 codex）。
// skill_scan（~/.agents/skills 与 $CODEX_HOME/skills，跳过 .system）→ 从 Runtime 导入（imported / failed / skipped）→
// 装配到 Agent → 派发 Run，独立 CODEX_HOME 里恰好出现该技能的文件（0600/0700），Run 结束后目录被删除 →
// 卸下后再派发 Run，不再出现。

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	alphaSkillMD = "---\nname: alpha\ndescription: Alpha skill\n---\nDo alpha things.\n"
	alphaRunSh   = "#!/bin/sh\necho alpha\n"
)

func writeLocalSkill(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, name, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type importItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (e *env) runtimeImport(spaceID, deviceID string, names []string) map[string]importItem {
	e.t.Helper()
	var created struct {
		CommandID string `json:"command_id"`
	}
	e.call(http.StatusAccepted, http.MethodPost, "spaces/"+spaceID+"/skills/runtime-imports", map[string]any{"device_id": deviceID, "names": names}, &created)
	var progress struct {
		Status string       `json:"status"`
		Items  []importItem `json:"items"`
	}
	waitFor(e.t, "runtime import finished", 60*time.Second, func() bool {
		e.call(http.StatusOK, http.MethodGet, "spaces/"+spaceID+"/skills/runtime-imports/"+created.CommandID, nil, &progress)
		return progress.Status != "running"
	})
	if progress.Status != "done" {
		e.t.Fatalf("import status = %s items = %+v", progress.Status, progress.Items)
	}
	result := map[string]importItem{}
	for _, item := range progress.Items {
		result[item.Name] = item
	}
	return result
}

func TestSkillsEndToEnd(t *testing.T) {
	e := newEnv(t)
	home := filepath.Join(e.work, "home")
	state := filepath.Join(e.work, "state")
	userCodex := filepath.Join(e.work, "user-codex-home")
	configDir := filepath.Join(e.work, "config")
	for _, dir := range []string{home, state, userCodex} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(userCodex, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(e.work, "codex-e2e.sh")
	if err := os.WriteFile(codexPath, []byte(strings.ReplaceAll(fakeCodex, "@STATE@", state)), 0o700); err != nil {
		t.Fatal(err)
	}
	// 本机技能：alpha 在 ~/.agents/skills（2 个文件），beta 在 $CODEX_HOME/skills，.system 内置缓存不应出现。
	writeLocalSkill(t, filepath.Join(home, ".agents", "skills"), "alpha", map[string]string{"SKILL.md": alphaSkillMD, "scripts/run.sh": alphaRunSh})
	writeLocalSkill(t, filepath.Join(userCodex, "skills"), "beta", map[string]string{"SKILL.md": "---\nname: beta\ndescription: Beta skill\n---\nBeta.\n"})
	writeLocalSkill(t, filepath.Join(userCodex, "skills"), ".system/x", map[string]string{"SKILL.md": "---\nname: x\ndescription: system\n---\n"})
	childEnv := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CODEX_HOME=" + userCodex, "TMPDIR=" + e.work}

	// 注册并启动 daemon。
	var issued struct {
		InstallToken string `json:"install_token"`
	}
	e.call(http.StatusCreated, http.MethodPost, "devices/registration-tokens", map[string]any{"name": "e2e-skills-mac", "scope": "ORGANIZATION"}, &issued)
	register := exec.CommandContext(e.ctx, e.binary, "computer", "register", "--install-token", issued.InstallToken,
		"--config-dir", configDir, "--no-service", "--supported-version", "0.0.1-e2e", "--runtime-executable", codexPath)
	register.Env = childEnv
	if output, err := register.CombinedOutput(); err != nil {
		t.Fatalf("register: %v\n%s", err, output)
	}
	var config struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(configDir, "config.json"))), &config); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(e.work, "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir, err := os.MkdirTemp("/tmp", "of5c-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	daemonCmd := exec.Command(e.binary, "daemon", "run", "--config", filepath.Join(configDir, "config.json"),
		"--socket", filepath.Join(runtimeDir, "d.sock"), "--heartbeat-interval", "500ms", "--offer-poll-interval", "1s")
	daemonCmd.Env, daemonCmd.Stdout, daemonCmd.Stderr = childEnv, logFile, logFile
	if err := daemonCmd.Start(); err != nil {
		t.Fatal(err)
	}
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- daemonCmd.Wait() }()
	t.Cleanup(func() {
		_ = daemonCmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-daemonDone:
		case <-time.After(30 * time.Second):
			_ = daemonCmd.Process.Kill()
			<-daemonDone
		}
		_ = logFile.Close()
	})
	var device struct {
		Name              string     `json:"name"`
		Online            bool       `json:"online"`
		RuntimesCheckedAt *time.Time `json:"runtimes_checked_at"`
	}
	waitFor(t, "device online", 60*time.Second, func() bool {
		device = struct {
			Name              string     `json:"name"`
			Online            bool       `json:"online"`
			RuntimesCheckedAt *time.Time `json:"runtimes_checked_at"`
		}{}
		e.call(http.StatusOK, http.MethodGet, "devices/"+config.DeviceID, nil, &device)
		return device.Online && device.RuntimesCheckedAt != nil
	})
	if device.Name == "" {
		t.Fatal("device name is empty")
	}

	// 1. skill_scan：alpha（2 个文件）与 beta，不含 .system。
	scan := e.runCommand(config.DeviceID, "skill_scan", map[string]any{})
	var scanResult struct {
		Supported bool `json:"supported"`
		Skills    []struct {
			Name       string `json:"name"`
			FileCount  int    `json:"file_count"`
			Importable bool   `json:"importable"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(scan.Result, &scanResult); err != nil || !scanResult.Supported || len(scanResult.Skills) != 2 ||
		scanResult.Skills[0].Name != "alpha" || scanResult.Skills[0].FileCount != 2 || !scanResult.Skills[0].Importable ||
		scanResult.Skills[1].Name != "beta" || !scanResult.Skills[1].Importable {
		t.Fatalf("skill_scan result = %s, %v", scan.Result, err)
	}

	// 2. 导入 alpha、beta 和不存在的 gamma。
	var space struct {
		ID string `json:"id"`
	}
	e.call(http.StatusCreated, http.MethodPost, "spaces", map[string]any{"name": "Skills " + e.org[:6], "visibility": "SHARED"}, &space)
	items := e.runtimeImport(space.ID, config.DeviceID, []string{"alpha", "beta", "gamma"})
	if items["alpha"].Status != "imported" || items["beta"].Status != "imported" || items["gamma"].Status != "failed" || items["gamma"].Reason != "NOT_FOUND" {
		t.Fatalf("import items = %+v\n%s", items, readFile(t, logPath))
	}
	var listed struct {
		Skills []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			SourceKind  string `json:"source_kind"`
			SourceLabel string `json:"source_label"`
			FileCount   int    `json:"file_count"`
		} `json:"skills"`
	}
	e.call(http.StatusOK, http.MethodGet, "spaces/"+space.ID+"/skills", nil, &listed)
	alphaID := ""
	for _, skill := range listed.Skills {
		if skill.SourceKind != "runtime" || skill.SourceLabel != device.Name {
			t.Fatalf("skill source = %+v, device name %q", skill, device.Name)
		}
		if skill.Name == "alpha" {
			alphaID = skill.ID
			if skill.FileCount != 2 {
				t.Fatalf("alpha file_count = %d", skill.FileCount)
			}
		}
	}
	if len(listed.Skills) != 2 || alphaID == "" {
		t.Fatalf("skills = %+v", listed.Skills)
	}

	// 3. 再导入一次 alpha：skipped（SKILL_NAME_TAKEN）。
	again := e.runtimeImport(space.ID, config.DeviceID, []string{"alpha"})
	if again["alpha"].Status != "skipped" || again["alpha"].Reason != "SKILL_NAME_TAKEN" {
		t.Fatalf("second import = %+v", again)
	}

	// 4. 装配 alpha 到 Agent，派发 Run。
	var grantID string
	e.scalar(&grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, e.org, config.DeviceID)
	var agent struct {
		ID string `json:"id"`
	}
	e.call(http.StatusCreated, http.MethodPost, "spaces/"+space.ID+"/agents", map[string]any{
		"name": "Skiller", "kind": "CODE", "visibility": "SHARED",
		"config": map[string]any{"computer_id": config.DeviceID, "grant_id": grantID, "runtime": "codex", "model": "default"},
	}, &agent)
	e.call(http.StatusOK, http.MethodPut, "spaces/"+space.ID+"/agents/"+agent.ID+"/skills", map[string]any{"skill_ids": []string{alphaID}}, nil)
	var conversation struct {
		ID string `json:"id"`
	}
	e.call(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": space.ID, "agent_id": agent.ID}, &conversation)
	sendAndWait := func(count int) {
		t.Helper()
		messageID := newID(t)
		e.call(http.StatusCreated, http.MethodPost, "conversations/"+conversation.ID+"/messages", map[string]any{
			"id": messageID, "client_message_id": "client-" + messageID, "body": "say OK", "mentions": []map[string]string{{"agent_id": agent.ID}},
		}, nil)
		waitFor(t, "run finished", 60*time.Second, func() bool {
			var done int
			e.scalar(&done, `SELECT count(*) FROM openfox.runs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid
				AND state IN ('SUCCEEDED', 'FAILED', 'CANCELED')`, e.org, conversation.ID)
			return done >= count
		})
		var failed int
		e.scalar(&failed, `SELECT count(*) FROM openfox.runs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND state <> 'SUCCEEDED'`, e.org, conversation.ID)
		if failed != 0 {
			t.Fatalf("a run did not succeed\n%s", readFile(t, logPath))
		}
	}
	sendAndWait(1)
	run1 := filepath.Join(state, "run-1")
	if got := readFile(t, filepath.Join(run1, "files")); got != "./alpha/SKILL.md\n./alpha/scripts/run.sh\n" {
		t.Fatalf("run-1 skill files = %q", got)
	}
	if readFile(t, filepath.Join(run1, "alpha-skill-md")) != alphaSkillMD || readFile(t, filepath.Join(run1, "alpha-run-sh")) != alphaRunSh {
		t.Fatalf("run-1 skill content mismatch: %q %q", readFile(t, filepath.Join(run1, "alpha-skill-md")), readFile(t, filepath.Join(run1, "alpha-run-sh")))
	}
	if bad := readFile(t, filepath.Join(run1, "badfiles")) + readFile(t, filepath.Join(run1, "baddirs")); bad != "" {
		t.Fatalf("unexpected permissions: %q", bad)
	}
	// Run 结束后该独立 CODEX_HOME 已删除（不是用户自己的 CODEX_HOME）。
	runHome := strings.TrimSpace(readFile(t, filepath.Join(run1, "home")))
	if runHome == "" || runHome == userCodex {
		t.Fatalf("run CODEX_HOME = %q", runHome)
	}
	waitFor(t, "run CODEX_HOME removed", 15*time.Second, func() bool {
		_, err := os.Stat(runHome)
		return os.IsNotExist(err)
	})

	// 5. 卸下 alpha 后再派发 Run：独立 CODEX_HOME 里没有 skills。
	e.call(http.StatusOK, http.MethodPut, "spaces/"+space.ID+"/agents/"+agent.ID+"/skills", map[string]any{"skill_ids": []string{}}, nil)
	sendAndWait(2)
	run2 := filepath.Join(state, "run-2")
	if strings.TrimSpace(readFile(t, filepath.Join(run2, "home"))) == "" {
		t.Fatal("second run was not recorded")
	}
	if _, err := os.Stat(filepath.Join(run2, "files")); !os.IsNotExist(err) {
		t.Fatalf("second run still has skills: %q", readFile(t, filepath.Join(run2, "files")))
	}
	entries, _ := os.ReadDir(state)
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if strings.Contains(strings.Join(names, ","), "run-3") {
		t.Fatalf("unexpected extra run records: %v", names)
	}
	// 本机技能目录没有被修改（导入只读）。
	if readFile(t, filepath.Join(home, ".agents", "skills", "alpha", "SKILL.md")) != alphaSkillMD {
		t.Fatal("local skill was modified")
	}
}
