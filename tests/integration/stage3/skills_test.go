package stage3

// 技能集成测试：权限、名称与上限、手动创建 / 上传 / 保存 / 删除、Agent 装配、从 Runtime 导入（设备会话模拟）、
// Run 下发、URL 导入的拒绝路径、开始使用与 skill.changed 事件。
// 复用本目录的夹具：真实 HTTP 处理器、登录会话、租户事务；需要存储的用例用 withAvatarStorage 指向 t.TempDir()。
// 其中 env.space 是私有空间：manager 为空间 OWNER，member 为空间 MEMBER，加入 other 为 VIEWER 后只读。

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"openfox/internal/market"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openfox/internal/deviceauth"
)

type skillAgentView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type skillView struct {
	ID          string  `json:"id"`
	SpaceID     string  `json:"space_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	SourceKind  string  `json:"source_kind"`
	SourceLabel string  `json:"source_label"`
	SourceURL   *string `json:"source_url"`
	FileCount   int     `json:"file_count"`
	TotalBytes  int64   `json:"total_bytes"`
	CreatedBy   struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"created_by"`
	UsedBy    []skillAgentView `json:"used_by"`
	UsedCount int              `json:"used_count"`
	CanEdit   bool             `json:"can_edit"`
	Version   int              `json:"version"`
}

type skillDetailView struct {
	skillView
	Files []struct {
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
	} `json:"files"`
	Agents []skillAgentView `json:"agents"`
}

func (d skillDetailView) paths() []string {
	paths := make([]string, len(d.Files))
	for i, file := range d.Files {
		paths[i] = file.Path
	}
	return paths
}

type skillListView struct {
	Skills    []skillView `json:"skills"`
	CanCreate bool        `json:"can_create"`
}

func skillsPath(spaceID string) string { return "spaces/" + spaceID + "/skills" }

// skillDoc 是带 frontmatter 的 SKILL.md 文本。
func skillDoc(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\nInstructions for " + name + ".\n"
}

type mpPart struct {
	field, filename, value string
	data                   []byte
}

func skillFilePart(filename string, data []byte) mpPart {
	return mpPart{field: "file", filename: filename, data: data}
}

func skillPathPart(path string) mpPart { return mpPart{field: "file_path", value: path} }

func skillMultipart(t *testing.T, parts []mpPart) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		if part.filename == "" {
			if err := writer.WriteField(part.field, part.value); err != nil {
				t.Fatal(err)
			}
			continue
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, part.field, part.filename))
		header.Set("Content-Type", "application/octet-stream")
		file, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(part.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func uploadSkillParts(t *testing.T, client *s3Client, spaceID string, parts ...mpPart) s3Response {
	t.Helper()
	body, contentType := skillMultipart(t, parts)
	return client.raw(http.MethodPost, skillsPath(spaceID)+"/upload", body, contentType)
}

func zipFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte(content))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// newSkill 手动创建技能，要求 201。
func (e *s3Env) newSkill(client *s3Client, spaceID, name string) skillDetailView {
	e.t.Helper()
	var detail skillDetailView
	client.must(http.StatusCreated, http.MethodPost, skillsPath(spaceID),
		map[string]any{"name": name, "description": "Skill " + name, "instructions": "Do " + name}, &detail)
	return detail
}

func (e *s3Env) skillFile(client *s3Client, spaceID, skillID, path string) (content string, binary bool) {
	e.t.Helper()
	var file struct {
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
		Binary    bool   `json:"binary"`
		Content   string `json:"content"`
	}
	client.must(http.StatusOK, http.MethodGet, skillsPath(spaceID)+"/"+skillID+"/files?path="+path, nil, &file)
	return file.Content, file.Binary
}

func (e *s3Env) saveSkill(client *s3Client, spaceID, skillID string, version int, changes ...map[string]any) s3Response {
	e.t.Helper()
	return client.do(http.MethodPut, skillsPath(spaceID)+"/"+skillID+"/files", map[string]any{"version": version, "changes": changes})
}

func (e *s3Env) skillEvents(spaceID string) int {
	return e.count(`SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid AND type = 'skill.changed' AND space_id = $2::uuid`,
		e.org, spaceID)
}

func TestSkillPermissionsAndNames(t *testing.T) {
	env := newS3Env(t)
	withAvatarStorage(env, t.TempDir())
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	env.addSpaceMember(space, s3AdminID, "MEMBER")
	member, manager, viewer := env.member(), env.manager(), env.other()
	base := skillsPath(space)

	before := env.skillEvents(space)
	created := env.newSkill(member, space, "pdf-tools")
	if created.SourceKind != "manual" || created.FileCount != 1 || !created.CanEdit || created.Version != 1 ||
		created.CreatedBy.ID != s3MemberID || len(created.UsedBy) != 0 || created.UsedCount != 0 || created.SourceURL != nil {
		t.Fatalf("created = %+v", created)
	}
	if got := created.paths(); len(got) != 1 || got[0] != "SKILL.md" {
		t.Fatalf("files = %v", got)
	}
	// 服务端拼出 SKILL.md：frontmatter name / description + 指令。
	content, binary := env.skillFile(member, space, created.ID, "SKILL.md")
	if binary || content != "---\nname: \"pdf-tools\"\ndescription: \"Skill pdf-tools\"\n---\n\nDo pdf-tools\n" {
		t.Fatalf("SKILL.md = %q binary %v", content, binary)
	}
	if env.skillEvents(space) <= before {
		t.Fatal("creating a skill must emit skill.changed")
	}

	// 名称规则与重名。
	for _, name := range []string{"", "Bad Name", "UPPER", "-lead", strings.Repeat("a", 65), "under_score", "中文"} {
		member.expectError(http.StatusBadRequest, "SKILL_NAME_INVALID", http.MethodPost, base, map[string]any{"name": name})
	}
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base,
		map[string]any{"name": "long-desc", "description": strings.Repeat("字", 1025)})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, map[string]any{"name": "ok", "unknown": 1})
	member.expectError(http.StatusConflict, "SKILL_NAME_TAKEN", http.MethodPost, base, map[string]any{"name": "pdf-tools"})
	manager.must(http.StatusCreated, http.MethodPost, base, map[string]any{"name": strings.Repeat("a", 64)}, nil)

	// 只读成员：能读，不能写；跨租户 404。
	var list skillListView
	viewer.must(http.StatusOK, http.MethodGet, base, nil, &list)
	if len(list.Skills) != 2 || list.CanCreate || list.Skills[0].CanEdit || list.Skills[1].CanEdit {
		t.Fatalf("viewer list = %+v", list)
	}
	viewer.must(http.StatusOK, http.MethodGet, base+"/"+created.ID, nil, nil)
	viewer.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, base, map[string]any{"name": "viewer-skill"})
	viewer.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPut, base+"/"+created.ID+"/files",
		map[string]any{"version": 1, "changes": []map[string]any{{"path": "a.md", "content": "x"}}})
	viewer.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodDelete, base+"/"+created.ID+"?version=1", nil)
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, base, nil)
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, base+"/"+created.ID, nil)
	env.outsider().expectError(http.StatusNotFound, "", http.MethodPost, base, map[string]any{"name": "cross-tenant"})
	// 别的组织里用本组织的技能 ID 也读不到。
	env.outsider().doIn(env.otherOrg, http.MethodGet, skillsPath(env.otherSpace.ID)+"/"+created.ID, nil).expectStatus(t, http.StatusNotFound)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, base+"/not-a-uuid", nil)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, base+"/"+s3NewID(t), nil)

	// 非创建者且非组织管理员不能改、删（即使是空间 OWNER）；创建者与组织管理员可以。
	manager.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPut, base+"/"+created.ID+"/files",
		map[string]any{"version": 1, "changes": []map[string]any{{"path": "a.md", "content": "x"}}})
	manager.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodDelete, base+"/"+created.ID+"?version=1", nil)
	list = skillListView{}
	manager.must(http.StatusOK, http.MethodGet, base, nil, &list)
	for _, item := range list.Skills {
		if item.CanEdit != (item.CreatedBy.ID == s3ManagerID) || !list.CanCreate {
			t.Fatalf("manager can_edit for %s = %v", item.Name, item.CanEdit)
		}
	}
	var saved skillDetailView
	env.admin().must(http.StatusOK, http.MethodPut, base+"/"+created.ID+"/files",
		map[string]any{"version": 1, "changes": []map[string]any{{"path": "notes.md", "content": "by admin"}}}, &saved)
	if saved.Version != 2 || saved.FileCount != 2 || !saved.CanEdit {
		t.Fatalf("admin save = %+v", saved)
	}
	member.must(http.StatusNoContent, http.MethodDelete, base+"/"+created.ID+"?version=2", nil, nil)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, base+"/"+created.ID, nil)

	// 停用空间：读可以，所有写操作 409 SPACE_RETIRED。
	keep := env.newSkill(member, space, "retire-me")
	env.exec(`UPDATE openfox.spaces SET status = 'ARCHIVED' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space)
	member.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, base, map[string]any{"name": "after-retire"})
	member.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPut, base+"/"+keep.ID+"/files",
		map[string]any{"version": 1, "changes": []map[string]any{{"path": "a.md", "content": "x"}}})
	member.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodDelete, base+"/"+keep.ID+"?version=1", nil)
	member.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, base+"/url-imports", map[string]any{"url": "https://example.com/SKILL.md"})
	uploadSkillParts(t, member, space, skillFilePart("a.md", []byte(skillDoc("retired-upload", "d")))).expectStatus(t, http.StatusConflict)
	list = skillListView{}
	member.must(http.StatusOK, http.MethodGet, base, nil, &list)
	if list.CanCreate || list.Skills[0].CanEdit {
		t.Fatalf("retired list = %+v", list)
	}
	env.exec(`UPDATE openfox.spaces SET status = 'ACTIVE' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space)

	// 没有存储：创建 / 上传 503，读文件 503。
	withoutStorage(env)
	env.member().expectError(http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", http.MethodPost, base, map[string]any{"name": "no-storage"})
	uploadSkillParts(t, env.member(), space, skillFilePart("a.md", []byte(skillDoc("no-storage", "d")))).expectStatus(t, http.StatusServiceUnavailable)
	env.member().expectError(http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", http.MethodGet, base+"/"+keep.ID+"/files?path=SKILL.md", nil)
}

func TestSkillLimitPerSpace(t *testing.T) {
	env := newS3Env(t)
	withAvatarStorage(env, t.TempDir())
	space := env.space.ID
	env.exec(`INSERT INTO openfox.skills (organization_id, space_id, name, source_kind, created_by, updated_by)
		SELECT $1::uuid, $2::uuid, 'bulk-' || g, 'manual', $3::uuid, $3::uuid FROM generate_series(1, 499) g`, env.org, space, s3MemberID)
	env.newSkill(env.member(), space, "the-500th")
	env.member().expectError(http.StatusConflict, "SKILL_LIMIT", http.MethodPost, skillsPath(space), map[string]any{"name": "the-501st"})
	uploadSkillParts(t, env.member(), space, skillFilePart("a.md", []byte(skillDoc("the-501st", "d")))).expectStatus(t, http.StatusConflict)
	var list skillListView
	env.member().must(http.StatusOK, http.MethodGet, skillsPath(space), nil, &list)
	if len(list.Skills) != 500 {
		t.Fatalf("list size = %d", len(list.Skills))
	}
	// 失败的创建不留下存储文件。
	if n := env.count(`SELECT count(*) FROM openfox.skills WHERE organization_id = $1::uuid AND name = 'the-501st'`, env.org); n != 0 {
		t.Fatalf("rows for rejected skill = %d", n)
	}
}

func TestSkillUploads(t *testing.T) {
	env := newS3Env(t)
	root := t.TempDir()
	withAvatarStorage(env, root)
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	member := env.member()

	// 单个 .md。
	var detail skillDetailView
	response := uploadSkillParts(t, member, space, skillFilePart("anything.md", []byte(skillDoc("md-upload", "From a markdown file"))))
	response.expectStatus(t, http.StatusCreated)
	response.decode(t, &detail)
	if detail.Name != "md-upload" || detail.Description != "From a markdown file" || detail.SourceKind != "upload" || detail.FileCount != 1 || detail.SourceLabel != "" {
		t.Fatalf("md upload = %+v", detail.skillView)
	}
	uploadSkillParts(t, member, space, skillFilePart("again.md", []byte(skillDoc("md-upload", "dup")))).expectStatus(t, http.StatusConflict)
	if env.count(`SELECT count(*) FROM openfox.skills WHERE organization_id = $1::uuid AND name = 'md-upload'`, env.org) != 1 {
		t.Fatal("duplicate upload must not create a second skill")
	}
	uploadSkillParts(t, member, space, skillFilePart("a.md", []byte(skillDoc("md-upload", "dup")))).decodeCode(t, "SKILL_NAME_TAKEN")
	uploadSkillParts(t, member, space, skillFilePart("a.md", []byte("# no frontmatter"))).decodeCode(t, "SKILL_INVALID")
	uploadSkillParts(t, member, space, skillFilePart("a.md", []byte(skillDoc("Bad Name", "d")))).decodeCode(t, "SKILL_NAME_INVALID")
	uploadSkillParts(t, member, space, skillFilePart("a.txt", []byte("x"))).decodeCode(t, "SKILL_INVALID")
	uploadSkillParts(t, member, space, mpPart{field: "other", value: "x"}).decodeCode(t, "INVALID_REQUEST")
	uploadSkillParts(t, env.other(), space, skillFilePart("a.md", []byte(skillDoc("viewer-up", "d")))).expectStatus(t, http.StatusForbidden)
	uploadSkillParts(t, env.outsider(), space, skillFilePart("a.md", []byte(skillDoc("viewer-up", "d")))).expectStatus(t, http.StatusNotFound)

	// zip：根目录有 SKILL.md；SKILL.md 在唯一一层子目录里（去掉前缀）。
	flat := zipFiles(t, map[string]string{"SKILL.md": skillDoc("zip-flat", "d"), "ref/a.md": "a", "scripts/run.sh": "echo"})
	response = uploadSkillParts(t, member, space, skillFilePart("flat.zip", flat))
	response.expectStatus(t, http.StatusCreated)
	response.decode(t, &detail)
	if detail.Name != "zip-flat" || fmt.Sprint(detail.paths()) != "[SKILL.md ref/a.md scripts/run.sh]" {
		t.Fatalf("flat zip = %v", detail.paths())
	}
	nested := zipFiles(t, map[string]string{"demo/SKILL.md": skillDoc("zip-nested", "d"), "demo/ref/b.md": "b"})
	response = uploadSkillParts(t, member, space, skillFilePart("nested.zip", nested))
	response.expectStatus(t, http.StatusCreated)
	response.decode(t, &detail)
	if detail.Name != "zip-nested" || fmt.Sprint(detail.paths()) != "[SKILL.md ref/b.md]" {
		t.Fatalf("nested zip = %v", detail.paths())
	}
	// zip 里的非法路径、缺少 SKILL.md、不是 zip。
	uploadSkillParts(t, member, space, skillFilePart("x.zip", zipFiles(t, map[string]string{"../SKILL.md": skillDoc("zip-evil", "d")}))).decodeCode(t, "SKILL_INVALID")
	uploadSkillParts(t, member, space, skillFilePart("x.zip", zipFiles(t, map[string]string{"a/b.md": "x"}))).decodeCode(t, "SKILL_INVALID")
	uploadSkillParts(t, member, space, skillFilePart("x.zip", []byte("not a zip"))).decodeCode(t, "SKILL_INVALID")
	// zip 炸弹：压缩后很小，解压超过 5 MB。
	bomb := zipFiles(t, map[string]string{"SKILL.md": skillDoc("zip-bomb", "d"), "zeros.bin": strings.Repeat("\x00", 5<<20+1)})
	uploadSkillParts(t, member, space, skillFilePart("bomb.zip", bomb)).decodeCode(t, "SKILL_TOO_LARGE")

	// 文件夹：多个 file，每个后跟 file_path；根目录被统一的前缀包住时去掉前缀；二进制文件按 binary 读取。
	binary := []byte{0xff, 0xfe, 0x00, 0x01}
	response = uploadSkillParts(t, member, space,
		skillFilePart("SKILL.md", []byte(skillDoc("folder-skill", "d"))), skillPathPart("myfolder/SKILL.md"),
		skillFilePart("data.bin", binary), skillPathPart("myfolder/assets/data.bin"),
		skillFilePart("notes.md", []byte("notes")), skillPathPart("myfolder/notes.md"))
	response.expectStatus(t, http.StatusCreated)
	response.decode(t, &detail)
	if detail.Name != "folder-skill" || fmt.Sprint(detail.paths()) != "[SKILL.md assets/data.bin notes.md]" || detail.FileCount != 3 {
		t.Fatalf("folder upload = %v", detail.paths())
	}
	var file struct {
		Binary  bool   `json:"binary"`
		Content string `json:"content"`
		Size    int    `json:"size_bytes"`
	}
	member.must(http.StatusOK, http.MethodGet, skillsPath(space)+"/"+detail.ID+"/files?path=assets/data.bin", nil, &file)
	if !file.Binary || file.Content != "" || file.Size != 4 {
		t.Fatalf("binary file = %+v", file)
	}
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, skillsPath(space)+"/"+detail.ID+"/files?path=missing.md", nil)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, skillsPath(space)+"/"+detail.ID+"/files?path=../x", nil)
	// 文件夹里缺少 file_path、路径越界。
	uploadSkillParts(t, member, space, skillFilePart("SKILL.md", []byte(skillDoc("no-path", "d"))), skillFilePart("b.md", []byte("b"))).decodeCode(t, "SKILL_INVALID")
	uploadSkillParts(t, member, space, skillFilePart("SKILL.md", []byte(skillDoc("evil-path", "d"))), skillPathPart("SKILL.md"),
		skillFilePart("b.md", []byte("b")), skillPathPart("../b.md")).decodeCode(t, "SKILL_INVALID")

	// 上限：100 个文件可以，101 个不行；合计 5 MB 可以，5 MB + 1 不行。
	files := []mpPart{skillFilePart("SKILL.md", []byte(skillDoc("hundred", "d"))), skillPathPart("SKILL.md")}
	for i := 1; i < 100; i++ {
		files = append(files, skillFilePart("f.txt", []byte("x")), skillPathPart(fmt.Sprintf("files/f%03d.txt", i)))
	}
	uploadSkillParts(t, member, space, files...).expectStatus(t, http.StatusCreated)
	files[0] = skillFilePart("SKILL.md", []byte(skillDoc("hundred-one", "d")))
	files = append(files, skillFilePart("f.txt", []byte("x")), skillPathPart("files/one-more.txt"))
	uploadSkillParts(t, member, space, files...).decodeCode(t, "SKILL_TOO_LARGE")

	doc := []byte(skillDoc("exact-five-mb", "d"))
	exact := append([]mpPart{}, skillFilePart("SKILL.md", doc), skillPathPart("SKILL.md"),
		skillFilePart("big.bin", make([]byte, 5<<20-len(doc))), skillPathPart("big.bin"))
	uploadSkillParts(t, member, space, exact...).expectStatus(t, http.StatusCreated)
	doc = []byte(skillDoc("over-five-mb", "d"))
	over := []mpPart{skillFilePart("SKILL.md", doc), skillPathPart("SKILL.md"),
		skillFilePart("big.bin", make([]byte, 5<<20-len(doc)+1)), skillPathPart("big.bin")}
	uploadSkillParts(t, member, space, over...).decodeCode(t, "SKILL_TOO_LARGE")
	uploadSkillParts(t, member, space, skillFilePart("big.md", append([]byte(skillDoc("over-md", "d")), make([]byte, 5<<20)...))).decodeCode(t, "SKILL_TOO_LARGE")
	// 被拒绝的上传不留下技能和存储文件。
	if env.count(`SELECT count(*) FROM openfox.skills WHERE organization_id = $1::uuid AND name IN ('hundred-one', 'over-five-mb', 'over-md', 'zip-bomb')`, env.org) != 0 {
		t.Fatal("rejected uploads must not create skills")
	}
	assertStoredFiles(t, env, root)
}

// decodeCode 断言错误码（状态码另由调用方判断时用 expectStatus）。
func (r s3Response) decodeCode(t *testing.T, code string) {
	t.Helper()
	if r.code() != code {
		t.Fatalf("response = %d %s, want code %s", r.Status, r.Body, code)
	}
}

// assertStoredFiles 要求存储目录里的技能文件与 skill_files 的记录一一对应（没有孤儿文件，也没有缺失）。
func assertStoredFiles(t *testing.T, env *s3Env, root string) {
	t.Helper()
	rows, err := env.superuser.Query(env.ctx, `SELECT file_key FROM openfox.skill_files WHERE organization_id = $1::uuid`, env.org)
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		recorded[key] = true
	}
	rows.Close()
	found := map[string]bool{}
	base := filepath.Join(root, "skills", env.org)
	_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			relative, _ := filepath.Rel(root, path)
			found[filepath.ToSlash(relative)] = true
		}
		return nil
	})
	for key := range recorded {
		if !found[key] {
			t.Errorf("recorded file %s is missing from storage", key)
		}
	}
	for key := range found {
		if !recorded[key] {
			t.Errorf("orphan storage file %s", key)
		}
	}
}

func TestSkillSaveFiles(t *testing.T) {
	env := newS3Env(t)
	root := t.TempDir()
	withAvatarStorage(env, root)
	space := env.space.ID
	member := env.member()
	base := skillsPath(space)
	first := env.newSkill(member, space, "first-skill")
	env.newSkill(member, space, "taken-name")
	path := base + "/" + first.ID + "/files"
	events := env.skillEvents(space)

	// 改 SKILL.md：名称和描述随 frontmatter 变化，新增文件，版本 +1。
	var saved skillDetailView
	member.must(http.StatusOK, http.MethodPut, path, map[string]any{"version": 1, "changes": []map[string]any{
		{"path": "SKILL.md", "content": skillDoc("renamed-skill", "New description")},
		{"path": "ref/guide.md", "content": "# Guide"},
	}}, &saved)
	if saved.Name != "renamed-skill" || saved.Description != "New description" || saved.Version != 2 || saved.FileCount != 2 ||
		fmt.Sprint(saved.paths()) != "[SKILL.md ref/guide.md]" {
		t.Fatalf("saved = %+v %v", saved.skillView, saved.paths())
	}
	if content, _ := env.skillFile(member, space, first.ID, "ref/guide.md"); content != "# Guide" {
		t.Fatalf("guide = %q", content)
	}
	if env.skillEvents(space) <= events {
		t.Fatal("saving must emit skill.changed")
	}
	assertStoredFiles(t, env, root)

	// 版本冲突、改名重名、不能删 SKILL.md、frontmatter 解析失败、非法路径、删除不存在的文件。
	member.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPut, path, map[string]any{"version": 1, "changes": []map[string]any{{"path": "a.md", "content": "x"}}})
	member.expectError(http.StatusConflict, "SKILL_NAME_TAKEN", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{
		{"path": "SKILL.md", "content": skillDoc("taken-name", "d")}}})
	member.expectError(http.StatusBadRequest, "SKILL_INVALID", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "SKILL.md", "delete": true}}})
	member.expectError(http.StatusBadRequest, "SKILL_INVALID", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "SKILL.md", "content": "no frontmatter"}}})
	member.expectError(http.StatusBadRequest, "SKILL_NAME_INVALID", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "SKILL.md", "content": skillDoc("Bad Name", "d")}}})
	member.expectError(http.StatusBadRequest, "SKILL_INVALID", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "../x", "content": "x"}}})
	member.expectError(http.StatusBadRequest, "SKILL_INVALID", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "/abs", "content": "x"}}})
	member.expectError(http.StatusBadRequest, "SKILL_INVALID", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "a//b", "content": "x"}}})
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "missing.md", "delete": true}}})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "a.md"}}})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "a.md", "content": "x", "delete": true}}})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{}})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, path, map[string]any{"version": 0, "changes": []map[string]any{{"path": "a.md", "content": "x"}}})
	member.expectError(http.StatusBadRequest, "SKILL_INVALID", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{
		{"path": "a.md", "content": "x"}, {"path": "a.md", "content": "y"}}})
	// 文件与目录同名。
	member.expectError(http.StatusBadRequest, "SKILL_INVALID", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "ref", "content": "x"}}})
	// 失败的保存没有改变任何东西。
	var unchanged skillDetailView
	member.must(http.StatusOK, http.MethodGet, base+"/"+first.ID, nil, &unchanged)
	if unchanged.Version != 2 || unchanged.Name != "renamed-skill" || unchanged.FileCount != 2 {
		t.Fatalf("after failed saves = %+v", unchanged.skillView)
	}
	assertStoredFiles(t, env, root)

	// 上限：超过 100 个文件、超过 5 MB。
	changes := []map[string]any{}
	for i := 0; i < 99; i++ {
		changes = append(changes, map[string]any{"path": fmt.Sprintf("many/f%03d.txt", i), "content": "x"})
	}
	member.expectError(http.StatusRequestEntityTooLarge, "SKILL_TOO_LARGE", http.MethodPut, path, map[string]any{"version": 2, "changes": changes})
	member.expectError(http.StatusRequestEntityTooLarge, "SKILL_TOO_LARGE", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{
		{"path": "big.txt", "content": strings.Repeat("a", 5<<20)}}})
	member.expectError(http.StatusRequestEntityTooLarge, "SKILL_TOO_LARGE", http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{
		{"path": "big.txt", "content": strings.Repeat("a", 5<<20+1)}}})
	member.must(http.StatusOK, http.MethodPut, path, map[string]any{"version": 2, "changes": changes[:98]}, &saved)
	if saved.FileCount != 100 || saved.Version != 3 {
		t.Fatalf("100 files = %+v", saved.skillView)
	}
	assertStoredFiles(t, env, root)

	// 删除文件：记录和存储文件都被清掉（未被引用的旧文件）。
	deletes := []map[string]any{{"path": "ref/guide.md", "delete": true}}
	for i := 0; i < 98; i++ {
		deletes = append(deletes, map[string]any{"path": fmt.Sprintf("many/f%03d.txt", i), "delete": true})
	}
	member.must(http.StatusOK, http.MethodPut, path, map[string]any{"version": 3, "changes": deletes}, &saved)
	if saved.FileCount != 1 || fmt.Sprint(saved.paths()) != "[SKILL.md]" || saved.Version != 4 {
		t.Fatalf("after deletes = %v %+v", saved.paths(), saved.skillView)
	}
	assertStoredFiles(t, env, root)

	// 删除技能：版本冲突不删；成功后文件记录、装配和存储文件都清理。
	agent := env.insertAgent(space)
	env.manager().must(http.StatusOK, http.MethodPut, "spaces/"+space+"/agents/"+agent+"/skills", map[string]any{"skill_ids": []string{first.ID}}, nil)
	member.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodDelete, base+"/"+first.ID+"?version=1", nil)
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodDelete, base+"/"+first.ID, nil)
	member.must(http.StatusNoContent, http.MethodDelete, base+"/"+first.ID+"?version=4", nil, nil)
	if env.count(`SELECT count(*) FROM openfox.agent_skills WHERE organization_id = $1::uuid AND skill_id = $2::uuid`, env.org, first.ID) != 0 ||
		env.count(`SELECT count(*) FROM openfox.skill_files WHERE organization_id = $1::uuid AND skill_id = $2::uuid`, env.org, first.ID) != 0 {
		t.Fatal("delete must cascade to files and Agent assignments")
	}
	var assigned struct {
		Skills []skillView `json:"skills"`
	}
	env.manager().must(http.StatusOK, http.MethodGet, "spaces/"+space+"/agents/"+agent+"/skills", nil, &assigned)
	if len(assigned.Skills) != 0 {
		t.Fatalf("assigned after delete = %+v", assigned.Skills)
	}
	assertStoredFiles(t, env, root)
	if _, err := os.Stat(filepath.Join(root, "skills", env.org, first.ID)); err == nil {
		entries, _ := os.ReadDir(filepath.Join(root, "skills", env.org, first.ID))
		if len(entries) != 0 {
			t.Fatalf("deleted skill left %d storage files", len(entries))
		}
	}
	_ = deviceauth.EncodeBase64URL
}

func TestSkillAssignListAndStartedSteps(t *testing.T) {
	env := newS3Env(t)
	withAvatarStorage(env, t.TempDir())
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	env.addSpaceMember(space, s3AdminID, "MEMBER")
	member, manager := env.member(), env.manager()
	agentA, agentB := env.insertAgent(space), env.insertAgent(space)
	agentPath := func(id string) string { return "spaces/" + space + "/agents/" + id + "/skills" }

	started := getStartedFor(t, env.owner())
	if done, available := started.step(t, "install_skill"); done || !available {
		t.Fatalf("install_skill before = %v %v", done, available)
	}
	s1 := env.newSkill(member, space, "alpha-skill")
	s2 := env.newSkill(manager, space, "beta-skill")
	env.newSkill(env.admin(), space, "gamma-skill")
	if done, _ := getStartedFor(t, env.owner()).step(t, "install_skill"); !done {
		t.Fatal("install_skill must complete after a skill exists")
	}
	if done, _ := getStartedFor(t, env.owner()).step(t, "attach_skill_to_agent"); done {
		t.Fatal("attach_skill_to_agent must stay open before assignment")
	}

	// 装配：manager 是空间 OWNER，能编辑共享 Agent；顺序即 position。
	events := env.skillEvents(space)
	var assigned struct {
		Skills []skillView `json:"skills"`
	}
	manager.must(http.StatusOK, http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": []string{s2.ID, s1.ID}}, &assigned)
	if len(assigned.Skills) != 2 || assigned.Skills[0].Name != "beta-skill" || assigned.Skills[1].Name != "alpha-skill" {
		t.Fatalf("assigned = %+v", assigned.Skills)
	}
	if env.skillEvents(space) <= events {
		t.Fatal("assignment must emit skill.changed")
	}
	manager.must(http.StatusOK, http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": []string{s1.ID, s2.ID}}, &assigned)
	if assigned.Skills[0].Name != "alpha-skill" {
		t.Fatalf("reordered = %+v", assigned.Skills)
	}
	manager.must(http.StatusOK, http.MethodPut, agentPath(agentB), map[string]any{"skill_ids": []string{s2.ID}}, nil)
	manager.must(http.StatusOK, http.MethodGet, agentPath(agentA), nil, &assigned)
	if len(assigned.Skills) != 2 || assigned.Skills[0].Name != "alpha-skill" {
		t.Fatalf("list assigned = %+v", assigned.Skills)
	}
	if done, _ := getStartedFor(t, env.owner()).step(t, "attach_skill_to_agent"); !done {
		t.Fatal("attach_skill_to_agent must complete after assignment")
	}

	// 校验：超过 20 个、重复、不存在、跨空间、缺字段。
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": []string{s1.ID, s1.ID}})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": []string{s3NewID(t)}})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": []string{"nope"}})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, agentPath(agentA), map[string]any{})
	otherSpace := env.createSpace("shared")
	env.addSpaceMember(otherSpace.ID, s3ManagerID, "OWNER")
	foreign := env.newSkill(manager, otherSpace.ID, "foreign-skill")
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": []string{foreign.ID}})
	ids := []string{}
	for i := 0; i < 21; i++ {
		ids = append(ids, s3NewID(t))
	}
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": ids})
	// 恰好 20 个可以。
	twenty := []string{}
	for i := 0; i < 20; i++ {
		var id string
		env.scalar(&id, `INSERT INTO openfox.skills (organization_id, space_id, name, source_kind, created_by, updated_by)
			VALUES ($1::uuid, $2::uuid, $3, 'manual', $4::uuid, $4::uuid) RETURNING id::text`, env.org, space, fmt.Sprintf("bulk-%02d", i), s3MemberID)
		twenty = append(twenty, id)
	}
	manager.must(http.StatusOK, http.MethodPut, agentPath(agentB), map[string]any{"skill_ids": twenty}, &assigned)
	if len(assigned.Skills) != 20 {
		t.Fatalf("20 skills = %d", len(assigned.Skills))
	}
	manager.must(http.StatusOK, http.MethodPut, agentPath(agentB), map[string]any{"skill_ids": []string{s2.ID}}, nil)

	// 权限：只读成员、非 Agent 管理者不能装配；跨租户 404；停用空间 409；不存在的 Agent 404。
	env.other().expectError(http.StatusForbidden, "", http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": []string{s1.ID}})
	member.expectError(http.StatusForbidden, "", http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": []string{s1.ID}})
	env.outsider().expectError(http.StatusNotFound, "", http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": []string{s1.ID}})
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, agentPath(agentA), nil)
	manager.expectError(http.StatusNotFound, "", http.MethodPut, agentPath(s3NewID(t)), map[string]any{"skill_ids": []string{}})
	env.other().must(http.StatusOK, http.MethodGet, agentPath(agentA), nil, nil)
	env.exec(`UPDATE openfox.spaces SET status = 'ARCHIVED' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space)
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPut, agentPath(agentA), map[string]any{"skill_ids": []string{s1.ID}})
	env.exec(`UPDATE openfox.spaces SET status = 'ACTIVE' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space)

	// 列表：usage 排序（装配它的 Agent 数）、used_by 最多 3 个、按创建者筛选、搜索（名称和描述，不区分大小写，% 按字面）。
	for i := 0; i < 4; i++ {
		extra := env.insertAgent(space)
		manager.must(http.StatusOK, http.MethodPut, agentPath(extra), map[string]any{"skill_ids": []string{s2.ID}}, nil)
	}
	var list skillListView
	member.must(http.StatusOK, http.MethodGet, skillsPath(space)+"?sort=usage", nil, &list)
	if list.Skills[0].Name != "beta-skill" || list.Skills[0].UsedCount != 6 || len(list.Skills[0].UsedBy) != 3 {
		t.Fatalf("usage sort first = %+v", list.Skills[0])
	}
	if list.Skills[1].Name != "alpha-skill" || list.Skills[1].UsedCount != 1 || len(list.Skills[1].UsedBy) != 1 {
		t.Fatalf("usage sort second = %+v", list.Skills[1])
	}
	var detail skillDetailView
	member.must(http.StatusOK, http.MethodGet, skillsPath(space)+"/"+s2.ID, nil, &detail)
	if len(detail.Agents) != 6 || len(detail.UsedBy) != 3 {
		t.Fatalf("detail agents = %d used_by = %d", len(detail.Agents), len(detail.UsedBy))
	}
	member.must(http.StatusOK, http.MethodGet, skillsPath(space)+"?created_by="+s3ManagerID, nil, &list)
	if len(list.Skills) != 1 || list.Skills[0].Name != "beta-skill" {
		t.Fatalf("created_by filter = %+v", list.Skills)
	}
	member.must(http.StatusOK, http.MethodGet, skillsPath(space)+"?q=GAMMA", nil, &list)
	if len(list.Skills) != 1 || list.Skills[0].Name != "gamma-skill" {
		t.Fatalf("name search = %+v", list.Skills)
	}
	member.must(http.StatusOK, http.MethodGet, skillsPath(space)+"?q=SKILL%20ALPHA", nil, &list)
	if len(list.Skills) != 1 || list.Skills[0].Name != "alpha-skill" {
		t.Fatalf("description search = %+v", list.Skills)
	}
	member.must(http.StatusOK, http.MethodGet, skillsPath(space)+"?q=nothing-like-this", nil, &list)
	if len(list.Skills) != 0 {
		t.Fatalf("empty search = %+v", list.Skills)
	}
	member.must(http.StatusOK, http.MethodGet, skillsPath(space)+"?q=%25", nil, &list)
	if len(list.Skills) != 0 {
		t.Fatalf("%% must be matched literally: %+v", list.Skills)
	}
	for _, query := range []string{"?sort=bad", "?created_by=nope", "?q=" + strings.Repeat("a", 101)} {
		member.expectError(http.StatusBadRequest, "", http.MethodGet, skillsPath(space)+query, nil)
	}
	env.exec(`UPDATE openfox.skills SET updated_at = clock_timestamp() + interval '1 hour' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, s1.ID)
	member.must(http.StatusOK, http.MethodGet, skillsPath(space)+"?sort=updated", nil, &list)
	if list.Skills[0].Name != "alpha-skill" {
		t.Fatalf("updated sort first = %s", list.Skills[0].Name)
	}

	// 删除技能：从所有 Agent 卸下。
	member.must(http.StatusNoContent, http.MethodDelete, skillsPath(space)+"/"+s1.ID+"?version=1", nil, nil)
	manager.must(http.StatusOK, http.MethodGet, agentPath(agentA), nil, &assigned)
	if len(assigned.Skills) != 1 || assigned.Skills[0].Name != "beta-skill" {
		t.Fatalf("after delete = %+v", assigned.Skills)
	}
}

func TestSkillURLImportRejectsInternalTargets(t *testing.T) {
	env := newS3Env(t)
	withAvatarStorage(env, t.TempDir())
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	member := env.member()
	path := skillsPath(space) + "/url-imports"
	for _, target := range []string{
		"https://127.0.0.1/", "https://127.0.0.1/SKILL.md", "https://localhost/SKILL.md", "https://[::1]/x", "https://10.1.2.3/x",
		"https://192.168.0.5/x", "https://169.254.169.254/latest/meta-data/", "https://100.64.0.1/x", "https://[::ffff:127.0.0.1]/x",
		"https://[fd00::1]/x", "http://example.com/SKILL.md", "ftp://example.com/x", "https://user:pw@example.com/x", "https://example.com:8443/x",
	} {
		member.expectError(http.StatusBadRequest, "SKILL_URL_FORBIDDEN", http.MethodPost, path, map[string]any{"url": target})
	}
	for _, target := range []string{"", "not a url", "example.com/SKILL.md", "https:///x"} {
		member.expectError(http.StatusBadRequest, "SKILL_URL_INVALID", http.MethodPost, path, map[string]any{"url": target})
	}
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, path, map[string]any{"url": "https://example.com/x", "extra": 1})
	// 授权先于下载：只读成员 403，跨租户 404，不会发起任何网络请求。
	env.other().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, path, map[string]any{"url": "https://example.com/SKILL.md"})
	env.outsider().expectError(http.StatusNotFound, "", http.MethodPost, path, map[string]any{"url": "https://example.com/SKILL.md"})
	if env.count(`SELECT count(*) FROM openfox.skills WHERE organization_id = $1::uuid`, env.org) != 0 {
		t.Fatal("rejected URL imports must not create skills")
	}
}

// ---- 设备会话模拟 ----

type skillDevice struct {
	ID    string
	token string
}

// rawJSON 发送不带用户会话的请求（设备侧）。
func (e *s3Env) rawJSON(method, path, bearer string, body any) s3Response {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(e.ctx, method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatal(err)
	}
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		e.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return s3Response{Status: response.StatusCode, Header: response.Header, Body: payload}
}

// registerSkillDevice 用 client 签发的注册令牌注册一台电脑（真实的注册 → 挑战签名 → 会话令牌流程）。
func (e *s3Env) registerSkillDevice(client *s3Client, org, scope string) skillDevice {
	e.t.Helper()
	var token struct {
		Token string `json:"token"`
	}
	response := client.doIn(org, http.MethodPost, "devices/registration-tokens", map[string]any{"name": "skill-" + s3Suffix(e.t), "scope": scope})
	if response.Status != http.StatusCreated {
		e.t.Fatalf("registration token = %d %s", response.Status, response.Body)
	}
	response.decode(e.t, &token)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		e.t.Fatal(err)
	}
	keyID := "skill-key-" + s3Suffix(e.t)
	registered := e.rawJSON(http.MethodPost, "/v1/device/register", "", map[string]string{
		"token": token.Token, "key_id": keyID, "public_key": deviceauth.EncodeBase64URL(public)})
	if registered.Status != http.StatusCreated {
		e.t.Fatalf("register = %d %s", registered.Status, registered.Body)
	}
	var device struct {
		DeviceID string `json:"device_id"`
	}
	registered.decode(e.t, &device)
	issued := e.rawJSON(http.MethodPost, "/v1/device/auth/challenge", "", map[string]string{"device_id": device.DeviceID, "key_id": keyID})
	if issued.Status != http.StatusCreated {
		e.t.Fatalf("challenge = %d %s", issued.Status, issued.Body)
	}
	var challenge struct {
		ChallengeID string `json:"challenge_id"`
		Nonce       string `json:"nonce"`
		Challenge   string `json:"challenge"`
	}
	issued.decode(e.t, &challenge)
	nonce, err := deviceauth.DecodeBase64URL(challenge.Nonce, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	payload, err := deviceauth.DecodeBase64URL(challenge.Challenge, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	signature := ed25519.Sign(private, deviceauth.CanonicalPayload(challenge.ChallengeID, device.DeviceID, keyID, nonce, payload))
	session := e.rawJSON(http.MethodPost, "/v1/device/auth/token", "", map[string]string{
		"challenge_id": challenge.ChallengeID, "device_id": device.DeviceID, "key_id": keyID, "signature": deviceauth.EncodeBase64URL(signature)})
	if session.Status != http.StatusCreated {
		e.t.Fatalf("session = %d %s", session.Status, session.Body)
	}
	var result struct {
		Token string `json:"token"`
	}
	session.decode(e.t, &result)
	return skillDevice{ID: device.DeviceID, token: result.Token}
}

func (e *s3Env) deviceCall(d skillDevice, method, path string, body any) s3Response {
	e.t.Helper()
	return e.rawJSON(method, path, d.token, body)
}

type importView struct {
	CommandID string `json:"command_id"`
	DeviceID  string `json:"device_id"`
	Status    string `json:"status"`
	Items     []struct {
		Name    string  `json:"name"`
		Status  string  `json:"status"`
		Reason  string  `json:"reason"`
		SkillID *string `json:"skill_id"`
	} `json:"items"`
}

func (v importView) item(t *testing.T, name string) (status, reason string, skillID *string) {
	t.Helper()
	for _, item := range v.Items {
		if item.Name == name {
			return item.Status, item.Reason, item.SkillID
		}
	}
	t.Fatalf("import has no item %s: %+v", name, v)
	return "", "", nil
}

func bundleOf(name string, files map[string]string) map[string]any {
	list := []map[string]string{}
	for path, content := range files {
		list = append(list, map[string]string{"path": path, "content_base64": base64.StdEncoding.EncodeToString([]byte(content))})
	}
	return map[string]any{"name": name, "files": list}
}

func (e *s3Env) deviceCommandPath(command string) string {
	return "/v1/device/organizations/" + e.org + "/commands/" + command
}

// startImport 以 client 发起导入，设备领取命令；返回导入视图。
func (e *s3Env) startImport(client *s3Client, space string, device skillDevice, names ...string) importView {
	e.t.Helper()
	var started importView
	client.must(http.StatusAccepted, http.MethodPost, skillsPath(space)+"/runtime-imports", map[string]any{"device_id": device.ID, "names": names}, &started)
	return started
}

func (e *s3Env) claim(device skillDevice, commandID string) {
	e.t.Helper()
	response := e.deviceCall(device, http.MethodPost, e.deviceCommandPath(commandID)+"/claim", nil)
	if response.Status != http.StatusOK {
		e.t.Fatalf("claim = %d %s", response.Status, response.Body)
	}
}

func TestSkillRuntimeImportFlow(t *testing.T) {
	env := newS3Env(t)
	withAvatarStorage(env, t.TempDir())
	ensureDirectory(env)
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	manager := env.manager()
	device := env.registerSkillDevice(manager, env.org, "PERSONAL")
	foreign := env.registerSkillDevice(env.outsider(), env.otherOrg, "")
	base := skillsPath(space) + "/runtime-imports"

	// 参数校验、权限。
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{}})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{"../x"}})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{"a", "a"}})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, map[string]any{"device_id": "nope", "names": []string{"a"}})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{"a"}, "x": 1})
	names := []string{}
	for i := 0; i < 21; i++ {
		names = append(names, fmt.Sprintf("n%02d", i))
	}
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": names})
	// 不是本人的电脑 404；只读成员 403；跨租户 404。
	env.member().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{"a"}})
	env.other().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{"a"}})
	env.outsider().expectError(http.StatusNotFound, "", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{"a"}})
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, base, map[string]any{"device_id": s3NewID(t), "names": []string{"a"}})
	// skill_import 只能经 createSkillRuntimeImport 下发。
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "devices/"+device.ID+"/commands",
		map[string]any{"kind": "skill_import", "params": map[string]any{"space_id": space, "names": []string{"a"}}})

	// 下发：3 个 pending 项；相同参数再次提交返回同一个命令；参数不同 409 COMMAND_BUSY（不复用）。
	started := env.startImport(manager, space, device, "alpha", "beta", "gamma")
	if started.Status != "running" || started.DeviceID != device.ID || len(started.Items) != 3 || started.Items[1].Name != "beta" {
		t.Fatalf("started = %+v", started)
	}
	for _, item := range started.Items {
		if item.Status != "pending" || item.Reason != "" || item.SkillID != nil {
			t.Fatalf("item = %+v", item)
		}
	}
	var again importView
	manager.must(http.StatusAccepted, http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{"alpha", "beta", "gamma"}}, &again)
	if again.CommandID != started.CommandID {
		t.Fatalf("same params must reuse the command: %s vs %s", again.CommandID, started.CommandID)
	}
	manager.expectError(http.StatusConflict, "COMMAND_BUSY", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{"alpha"}})
	manager.expectError(http.StatusConflict, "COMMAND_BUSY", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{"gamma", "beta", "alpha"}})
	if env.count(`SELECT count(*) FROM openfox.skill_import_items WHERE organization_id = $1::uuid AND command_id = $2::uuid`, env.org, started.CommandID) != 3 {
		t.Fatal("reused command must keep exactly 3 items")
	}

	// 只有发起人能读进度 / 取消。
	for _, other := range []*s3Client{env.member(), env.owner()} {
		other.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, base+"/"+started.CommandID, nil)
		other.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, base+"/"+started.CommandID+"/cancel", nil)
	}
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, base+"/"+s3NewID(t), nil)
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, base+"/not-a-uuid", nil)

	// 设备：列出待领取命令，领取（执行截止 = 领取 + 5 分钟 + 30 秒余量），参数为 {space_id, names}。
	listed := env.deviceCall(device, http.MethodGet, "/v1/device/organizations/"+env.org+"/commands?status=PENDING", nil)
	var pending struct {
		Commands []struct {
			ID     string          `json:"id"`
			Kind   string          `json:"kind"`
			Params json.RawMessage `json:"params"`
		} `json:"commands"`
	}
	listed.decode(t, &pending)
	if len(pending.Commands) != 1 || pending.Commands[0].ID != started.CommandID || pending.Commands[0].Kind != "skill_import" {
		t.Fatalf("pending = %s", listed.Body)
	}
	var params struct {
		SpaceID string   `json:"space_id"`
		Names   []string `json:"names"`
	}
	if err := json.Unmarshal(pending.Commands[0].Params, &params); err != nil || params.SpaceID != space || fmt.Sprint(params.Names) != "[alpha beta gamma]" {
		t.Fatalf("params = %s", pending.Commands[0].Params)
	}
	// 设备端要求 params 正好是 {space_id, names}，没有未知键。
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(pending.Commands[0].Params, &keys); err != nil || len(keys) != 2 {
		t.Fatalf("params keys = %s", pending.Commands[0].Params)
	}
	env.claim(device, started.CommandID)
	var seconds float64
	env.scalar(&seconds, `SELECT extract(epoch FROM expires_at - delivered_at)::float8 FROM openfox.device_commands WHERE id = $1::uuid`, started.CommandID)
	if seconds != 330 {
		t.Fatalf("skill_import deadline = %v seconds, want 330", seconds)
	}

	upload := func(d skillDevice, body any) s3Response {
		return env.deviceCall(d, http.MethodPost, env.deviceCommandPath(started.CommandID)+"/skills", body)
	}
	// 其他电脑 / 其他组织的电脑 404。
	other := env.registerSkillDevice(env.owner(), env.org, "ORGANIZATION")
	upload(other, bundleOf("alpha", map[string]string{"SKILL.md": skillDoc("alpha", "d")})).expectStatus(t, http.StatusNotFound)
	upload(foreign, bundleOf("alpha", map[string]string{"SKILL.md": skillDoc("alpha", "d")})).expectStatus(t, http.StatusNotFound)
	if upload(skillDevice{token: "bad"}, bundleOf("alpha", nil)).Status != http.StatusUnauthorized {
		t.Fatal("bad token must be 401")
	}
	// 名称不在 names 里 400；字段非法 400。
	upload(device, bundleOf("delta", map[string]string{"SKILL.md": skillDoc("delta", "d")})).expectStatus(t, http.StatusBadRequest)
	upload(device, map[string]any{"name": "../x", "files": []any{}}).expectStatus(t, http.StatusBadRequest)
	upload(device, map[string]any{"name": "alpha", "files": []any{}, "error_code": "lower"}).expectStatus(t, http.StatusBadRequest)

	// 成功：以发起人的身份建技能，来源为 runtime，来源说明为电脑名。
	var item struct {
		Name    string  `json:"name"`
		Status  string  `json:"status"`
		Reason  string  `json:"reason"`
		SkillID *string `json:"skill_id"`
	}
	response := upload(device, bundleOf("alpha", map[string]string{"SKILL.md": skillDoc("alpha-imported", "From my laptop"), "scripts/run.sh": "echo hi"}))
	response.expectStatus(t, http.StatusOK)
	response.decode(t, &item)
	if item.Status != "imported" || item.SkillID == nil || item.Reason != "" {
		t.Fatalf("alpha = %+v", item)
	}
	var imported skillDetailView
	manager.must(http.StatusOK, http.MethodGet, skillsPath(space)+"/"+*item.SkillID, nil, &imported)
	var deviceName string
	env.scalar(&deviceName, `SELECT name FROM openfox.physical_devices WHERE id = $1::uuid`, device.ID)
	if imported.Name != "alpha-imported" || imported.SourceKind != "runtime" || imported.SourceLabel != deviceName ||
		imported.CreatedBy.ID != s3ManagerID || imported.FileCount != 2 || imported.Description != "From my laptop" {
		t.Fatalf("imported = %+v", imported.skillView)
	}
	// 重复上传同一项是幂等的（设备重试）：返回同一个结果，不再建技能。
	response = upload(device, bundleOf("alpha", map[string]string{"SKILL.md": skillDoc("alpha-imported", "From my laptop")}))
	response.expectStatus(t, http.StatusOK)
	if env.count(`SELECT count(*) FROM openfox.skills WHERE organization_id = $1::uuid AND name = 'alpha-imported'`, env.org) != 1 {
		t.Fatal("repeated upload must not create another skill")
	}
	// 重名 → skipped（SKILL_NAME_TAKEN）。
	env.newSkill(env.member(), space, "beta-imported")
	response = upload(device, bundleOf("beta", map[string]string{"SKILL.md": skillDoc("beta-imported", "dup")}))
	response.expectStatus(t, http.StatusOK)
	response.decode(t, &item)
	if item.Status != "skipped" || item.Reason != "SKILL_NAME_TAKEN" || item.SkillID != nil {
		t.Fatalf("beta = %+v", item)
	}
	// 本机无法打包：带 error_code 且 files 为空 → failed。
	response = upload(device, map[string]any{"name": "gamma", "files": []any{}, "error_code": "NOT_FOUND"})
	response.expectStatus(t, http.StatusOK)
	response.decode(t, &item)
	if item.Status != "failed" || item.Reason != "NOT_FOUND" {
		t.Fatalf("gamma = %+v", item)
	}
	if env.count(`SELECT count(*) FROM openfox.skill_files WHERE organization_id = $1::uuid AND skill_id = $2::uuid`, env.org, imported.ID) != 2 {
		t.Fatal("imported files are missing")
	}

	// 回执后进度为 done，三项终态都在。
	report := env.deviceCall(device, http.MethodPost, env.deviceCommandPath(started.CommandID)+"/result",
		map[string]any{"status": "SUCCEEDED", "result": map[string]any{"imported": 1, "skipped": 1, "failed": 1}})
	report.expectStatus(t, http.StatusOK)
	var done importView
	manager.must(http.StatusOK, http.MethodGet, base+"/"+started.CommandID, nil, &done)
	if done.Status != "done" {
		t.Fatalf("done = %+v", done)
	}
	if status, _, id := done.item(t, "alpha"); status != "imported" || id == nil {
		t.Fatalf("alpha = %s %v", status, id)
	}
	if status, reason, _ := done.item(t, "beta"); status != "skipped" || reason != "SKILL_NAME_TAKEN" {
		t.Fatalf("beta = %s %s", status, reason)
	}
	if status, reason, _ := done.item(t, "gamma"); status != "failed" || reason != "NOT_FOUND" {
		t.Fatalf("gamma = %s %s", status, reason)
	}
	// 命令已完成后再上传 → 409。
	upload(device, bundleOf("alpha", map[string]string{"SKILL.md": skillDoc("late", "d")})).expectStatus(t, http.StatusConflict)
}

func TestSkillRuntimeImportCancelExpireAndFailure(t *testing.T) {
	env := newS3Env(t)
	withAvatarStorage(env, t.TempDir())
	ensureDirectory(env)
	space := env.space.ID
	manager := env.manager()
	device := env.registerSkillDevice(manager, env.org, "PERSONAL")
	base := skillsPath(space) + "/runtime-imports"
	upload := func(commandID string, body any) s3Response {
		return env.deviceCall(device, http.MethodPost, env.deviceCommandPath(commandID)+"/skills", body)
	}
	finish := func(commandID string) {
		env.deviceCall(device, http.MethodPost, env.deviceCommandPath(commandID)+"/result",
			map[string]any{"status": "SUCCEEDED", "result": map[string]any{"imported": 0, "skipped": 0, "failed": 0}}).expectStatus(t, http.StatusOK)
	}

	// 取消：pending 项改为 canceled，之后的上传 409 IMPORT_CANCELED；取消幂等。
	started := env.startImport(manager, space, device, "one", "two")
	env.claim(device, started.CommandID)
	upload(started.CommandID, bundleOf("one", map[string]string{"SKILL.md": skillDoc("one-skill", "d")})).expectStatus(t, http.StatusOK)
	var canceled importView
	manager.must(http.StatusOK, http.MethodPost, base+"/"+started.CommandID+"/cancel", nil, &canceled)
	if canceled.Status != "canceled" {
		t.Fatalf("canceled = %+v", canceled)
	}
	if status, _, _ := canceled.item(t, "one"); status != "imported" {
		t.Fatalf("finished item must stay imported, got %s", status)
	}
	if status, _, _ := canceled.item(t, "two"); status != "canceled" {
		t.Fatalf("pending item must become canceled, got %s", status)
	}
	upload(started.CommandID, bundleOf("two", map[string]string{"SKILL.md": skillDoc("two-skill", "d")})).decodeCode(t, "IMPORT_CANCELED")
	if env.count(`SELECT count(*) FROM openfox.skills WHERE organization_id = $1::uuid AND name = 'two-skill'`, env.org) != 0 {
		t.Fatal("upload after cancel must not create a skill")
	}
	manager.must(http.StatusOK, http.MethodPost, base+"/"+started.CommandID+"/cancel", nil, &canceled)
	if canceled.Status != "canceled" {
		t.Fatalf("second cancel = %+v", canceled)
	}
	finish(started.CommandID)

	// 内容不合规的项记为 failed（原因码），不影响其他项。
	started = env.startImport(manager, space, device, "bad-path", "no-entry", "bad-name", "bad-base64", "huge")
	env.claim(device, started.CommandID)
	cases := map[string]struct {
		body   any
		reason string
	}{
		"bad-path":   {bundleOf("bad-path", map[string]string{"SKILL.md": skillDoc("bp", "d"), "../x": "x"}), "SKILL_INVALID"},
		"no-entry":   {bundleOf("no-entry", map[string]string{"a.md": "x"}), "SKILL_INVALID"},
		"bad-name":   {bundleOf("bad-name", map[string]string{"SKILL.md": skillDoc("Bad Name", "d")}), "SKILL_INVALID"},
		"bad-base64": {map[string]any{"name": "bad-base64", "files": []map[string]string{{"path": "SKILL.md", "content_base64": "!!!"}}}, "SKILL_INVALID"},
		"huge": {map[string]any{"name": "huge", "files": []map[string]string{
			{"path": "SKILL.md", "content_base64": base64.StdEncoding.EncodeToString([]byte(skillDoc("huge", "d")))},
			{"path": "big.bin", "content_base64": base64.StdEncoding.EncodeToString(make([]byte, 5<<20))}}}, "SKILL_TOO_LARGE"},
	}
	for name, item := range cases {
		var result struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		}
		response := upload(started.CommandID, item.body)
		response.expectStatus(t, http.StatusOK)
		response.decode(t, &result)
		if result.Status != "failed" || result.Reason != item.reason {
			t.Fatalf("%s = %+v", name, result)
		}
	}
	finish(started.CommandID)

	// 过期：执行截止之后，进度读取时 pending 项改为 failed（COMMAND_EXPIRED），上传 409。
	started = env.startImport(manager, space, device, "slow-one", "slow-two")
	env.claim(device, started.CommandID)
	env.exec(`UPDATE openfox.device_commands SET created_at = created_at - interval '1 hour', delivered_at = delivered_at - interval '1 hour', expires_at = expires_at - interval '1 hour' WHERE id = $1::uuid`, started.CommandID)
	upload(started.CommandID, bundleOf("slow-one", map[string]string{"SKILL.md": skillDoc("slow-one", "d")})).decodeCode(t, "COMMAND_CONFLICT")
	var expired importView
	manager.must(http.StatusOK, http.MethodGet, base+"/"+started.CommandID, nil, &expired)
	if expired.Status != "done" {
		t.Fatalf("expired = %+v", expired)
	}
	for _, name := range []string{"slow-one", "slow-two"} {
		if status, reason, _ := expired.item(t, name); status != "failed" || reason != "COMMAND_EXPIRED" {
			t.Fatalf("%s = %s %s", name, status, reason)
		}
	}
	// 未领取（电脑离线）的命令过期同样处理。
	started = env.startImport(manager, space, device, "offline")
	env.exec(`UPDATE openfox.device_commands SET created_at = created_at - interval '1 hour', delivered_at = delivered_at - interval '1 hour', expires_at = expires_at - interval '1 hour' WHERE id = $1::uuid`, started.CommandID)
	manager.must(http.StatusOK, http.MethodGet, base+"/"+started.CommandID, nil, &expired)
	if status, reason, _ := expired.item(t, "offline"); status != "failed" || reason != "COMMAND_EXPIRED" || expired.Status != "done" {
		t.Fatalf("offline = %+v", expired)
	}
	// 电脑回执 SUCCEEDED 但仍有没上传的项（网络错误 / 5xx 后继续）：读取进度时剩余项改为 failed（UPLOAD_FAILED）。
	started = env.startImport(manager, space, device, "partial-one", "partial-two")
	env.claim(device, started.CommandID)
	upload(started.CommandID, bundleOf("partial-one", map[string]string{"SKILL.md": skillDoc("partial-one", "d")})).expectStatus(t, http.StatusOK)
	finish(started.CommandID)
	var partial importView
	manager.must(http.StatusOK, http.MethodGet, base+"/"+started.CommandID, nil, &partial)
	if status, _, _ := partial.item(t, "partial-one"); status != "imported" || partial.Status != "done" {
		t.Fatalf("partial = %+v", partial)
	}
	if status, reason, _ := partial.item(t, "partial-two"); status != "failed" || reason != "UPLOAD_FAILED" {
		t.Fatalf("partial-two = %s %s", status, reason)
	}
	// 413：请求体超过 8 MB 时返回 413，该项保持 pending（设备记 failed 后继续）。
	started = env.startImport(manager, space, device, "toobig")
	env.claim(device, started.CommandID)
	upload(started.CommandID, map[string]any{"name": "toobig", "files": []map[string]string{{"path": "SKILL.md", "content_base64": strings.Repeat("A", 8<<20+1)}}}).expectStatus(t, http.StatusRequestEntityTooLarge)
	var stillPending importView
	manager.must(http.StatusOK, http.MethodGet, base+"/"+started.CommandID, nil, &stillPending)
	if status, _, _ := stillPending.item(t, "toobig"); status != "pending" || stillPending.Status != "running" {
		t.Fatalf("stillPending = %+v", stillPending)
	}
	finish(started.CommandID)

	// 过期后可以重新发起（新命令）；命令失败：剩余项 failed（COMMAND_FAILED）。
	started = env.startImport(manager, space, device, "retry")
	env.claim(device, started.CommandID)
	env.deviceCall(device, http.MethodPost, env.deviceCommandPath(started.CommandID)+"/result",
		map[string]any{"status": "FAILED", "error_code": "SCAN_ERROR"}).expectStatus(t, http.StatusOK)
	var failed importView
	manager.must(http.StatusOK, http.MethodGet, base+"/"+started.CommandID, nil, &failed)
	if status, reason, _ := failed.item(t, "retry"); status != "failed" || reason != "COMMAND_FAILED" || failed.Status != "done" {
		t.Fatalf("failed = %+v", failed)
	}

	// 发起人在上传时已失去写权限（空间停用）：该项记为 failed（SPACE_RETIRED），不建技能。
	started = env.startImport(manager, space, device, "late-one")
	env.claim(device, started.CommandID)
	env.exec(`UPDATE openfox.spaces SET status = 'ARCHIVED' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space)
	var result struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	response := upload(started.CommandID, bundleOf("late-one", map[string]string{"SKILL.md": skillDoc("late-one", "d")}))
	response.expectStatus(t, http.StatusOK)
	response.decode(t, &result)
	if result.Status != "failed" || result.Reason != "SPACE_RETIRED" {
		t.Fatalf("retired = %+v", result)
	}
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, base, map[string]any{"device_id": device.ID, "names": []string{"x"}})
	env.exec(`UPDATE openfox.spaces SET status = 'ACTIVE' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space)
	finish(started.CommandID)

	// 发起人被移出空间后：failed（SPACE_NOT_FOUND）。
	started = env.startImport(manager, space, device, "removed-one")
	env.claim(device, started.CommandID)
	env.exec(`UPDATE openfox.organization_members SET status = 'REVOKED' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, env.org, s3ManagerID)
	response = upload(started.CommandID, bundleOf("removed-one", map[string]string{"SKILL.md": skillDoc("removed-one", "d")}))
	response.expectStatus(t, http.StatusOK)
	response.decode(t, &result)
	if result.Status != "failed" || result.Reason != "REQUESTER_INACTIVE" {
		t.Fatalf("inactive = %+v", result)
	}
	for _, name := range []string{"late-one", "removed-one", "two-skill"} {
		if env.count(`SELECT count(*) FROM openfox.skills WHERE organization_id = $1::uuid AND name = $2`, env.org, name) != 0 {
			t.Fatalf("%s must not have been created", name)
		}
	}
}

func TestSkillScanCommandLimits(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	manager := env.manager()
	device := env.registerSkillDevice(manager, env.org, "PERSONAL")
	commandsPath := "devices/" + device.ID + "/commands"

	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, commandsPath, map[string]any{"kind": "skill_scan", "params": map[string]any{"x": 1}})
	var created struct {
		Command struct {
			ID string `json:"id"`
		} `json:"command"`
	}
	manager.must(http.StatusAccepted, http.MethodPost, commandsPath, map[string]any{"kind": "skill_scan"}, &created)
	env.claim(device, created.Command.ID)
	var seconds float64
	env.scalar(&seconds, `SELECT extract(epoch FROM expires_at - delivered_at)::float8 FROM openfox.device_commands WHERE id = $1::uuid`, created.Command.ID)
	if seconds != 60 {
		t.Fatalf("skill_scan deadline = %v seconds, want 60", seconds)
	}
	// skill_scan 的结果可以到 64 KB，超过则 400；其他种类仍是 16 KB。
	skills := []map[string]any{}
	for i := 0; i < 100; i++ {
		skills = append(skills, map[string]any{"name": fmt.Sprintf("skill-%03d", i), "description": strings.Repeat("d", 160), "file_count": 3, "total_bytes": 1000, "importable": true})
	}
	result := func(pad int) map[string]any {
		return map[string]any{"supported": true, "runtime": "codex", "skills": skills, "pad": strings.Repeat("x", pad)}
	}
	resultPath := env.deviceCommandPath(created.Command.ID) + "/result"
	env.deviceCall(device, http.MethodPost, resultPath, map[string]any{"status": "SUCCEEDED", "result": result(70000)}).expectStatus(t, http.StatusBadRequest)
	// 迁移的 CHECK 限制的是 jsonb 的存储大小（≤ 64 KB），含大量小对象的结果比文本约大 5%：
	// 文本 60 KB 的密集结果可以通过；文本约 63 KB 的密集结果超过存储上限时返回 400，而不是 500。
	encoded, _ := json.Marshal(result(0))
	env.deviceCall(device, http.MethodPost, resultPath, map[string]any{"status": "SUCCEEDED", "result": result(60*1000 - len(encoded))}).expectStatus(t, http.StatusOK)

	manager.must(http.StatusAccepted, http.MethodPost, commandsPath, map[string]any{"kind": "skill_scan"}, &created)
	env.claim(device, created.Command.ID)
	resultPath = env.deviceCommandPath(created.Command.ID) + "/result"
	env.deviceCall(device, http.MethodPost, resultPath, map[string]any{"status": "SUCCEEDED", "result": result(63*1000 - len(encoded))}).expectStatus(t, http.StatusBadRequest)
	env.deviceCall(device, http.MethodPost, resultPath, map[string]any{"status": "SUCCEEDED", "result": result(0)}).expectStatus(t, http.StatusOK)

	manager.must(http.StatusAccepted, http.MethodPost, commandsPath, map[string]any{"kind": "runtime_probe"}, &created)
	env.claim(device, created.Command.ID)
	resultPath = env.deviceCommandPath(created.Command.ID) + "/result"
	env.deviceCall(device, http.MethodPost, resultPath, map[string]any{"status": "SUCCEEDED", "result": map[string]any{"pad": strings.Repeat("x", 17000)}}).expectStatus(t, http.StatusBadRequest)
	env.deviceCall(device, http.MethodPost, resultPath, map[string]any{"status": "SUCCEEDED", "result": map[string]any{"pad": "ok"}}).expectStatus(t, http.StatusOK)
	_ = time.Second
}

// ---- Run 下发 ----

func TestSkillRunDelivery(t *testing.T) {
	marketEnv := newMarketEnv(t)
	marketEnv.mustImport(market.ImportOptions{})
	env := marketEnv.s3Env
	ensureDirectory(env)
	space := env.space.ID
	owner, manager := env.owner(), env.manager()
	executor := env.registerSkillDevice(owner, env.org, "ORGANIZATION")
	bystander := env.registerSkillDevice(owner, env.org, "ORGANIZATION")
	foreign := env.registerSkillDevice(env.outsider(), env.otherOrg, "")

	var grantID string
	env.scalar(&grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, env.org, executor.ID)
	var agent struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/agents", map[string]any{
		"name": "Coder", "kind": "CODE", "visibility": "SHARED",
		"config": map[string]any{"computer_id": executor.ID, "grant_id": grantID, "runtime": "codex", "model": "gpt-5"},
	}, &agent)
	var conversation struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": space, "agent_id": agent.ID}, &conversation)
	runID := s3NewID(t)
	env.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id,
			config_version, grant_version, actor_id, state, lease_expires_at)
		SELECT c.organization_id, $2::uuid, c.space_id, c.id, c.agent_id, a.device_id, g.id,
		       a.current_config_version, g.permission_version, $4::uuid, 'PREPARING', clock_timestamp() + interval '5 minutes'
		  FROM openfox.conversations c
		  JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		  JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE c.organization_id = $1::uuid AND c.id = $3::uuid`, env.org, runID, conversation.ID, s3OwnerID)
	contextPath := "/v1/device/organizations/" + env.org + "/runs/" + runID + "/context?generation=1"
	skillPath := func(id string) string {
		return "/v1/device/organizations/" + env.org + "/runs/" + runID + "/skills/" + id + "?generation=1"
	}
	type runContext struct {
		Skills []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Version    int    `json:"version"`
			FileCount  int    `json:"file_count"`
			TotalBytes int    `json:"total_bytes"`
		} `json:"skills"`
	}

	// 没有装配：skills 是空数组而不是 null。
	response := env.deviceCall(executor, http.MethodGet, contextPath, nil)
	response.expectStatus(t, http.StatusOK)
	if !strings.Contains(string(response.Body), `"skills":[]`) {
		t.Fatalf("empty skills must be [], body = %s", response.Body)
	}

	first := env.newSkill(manager, space, "first-skill")
	var second skillDetailView
	response = uploadSkillParts(t, manager, space, skillFilePart("SKILL.md", []byte(skillDoc("second-skill", "d"))), skillPathPart("second-skill/SKILL.md"),
		skillFilePart("a.md", []byte("reference a")), skillPathPart("second-skill/ref/a.md"))
	response.expectStatus(t, http.StatusCreated)
	response.decode(t, &second)
	owner.must(http.StatusOK, http.MethodPut, "spaces/"+space+"/agents/"+agent.ID+"/skills", map[string]any{"skill_ids": []string{second.ID, first.ID}}, nil)

	var ctxBody runContext
	env.deviceCall(executor, http.MethodGet, contextPath, nil).decode(t, &ctxBody)
	if len(ctxBody.Skills) != 2 || ctxBody.Skills[0].Name != "second-skill" || ctxBody.Skills[0].ID != second.ID || ctxBody.Skills[0].FileCount != 2 ||
		ctxBody.Skills[0].Version != 1 || ctxBody.Skills[1].Name != "first-skill" || ctxBody.Skills[0].TotalBytes != int(second.TotalBytes) {
		t.Fatalf("context skills = %+v", ctxBody.Skills)
	}

	// 下载：技能包、禁止缓存。
	response = env.deviceCall(executor, http.MethodGet, skillPath(second.ID), nil)
	response.expectStatus(t, http.StatusOK)
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header.Get("Cache-Control"))
	}
	var bundle struct {
		Name  string `json:"name"`
		Files []struct {
			Path          string `json:"path"`
			ContentBase64 string `json:"content_base64"`
		} `json:"files"`
	}
	response.decode(t, &bundle)
	if bundle.Name != ctxBody.Skills[0].Name {
		t.Fatalf("bundle name %q must equal the manifest name %q", bundle.Name, ctxBody.Skills[0].Name)
	}
	if bundle.Name != "second-skill" || len(bundle.Files) != 2 || bundle.Files[0].Path != "SKILL.md" || bundle.Files[1].Path != "ref/a.md" {
		t.Fatalf("bundle = %+v", bundle)
	}
	if text, _ := base64.StdEncoding.DecodeString(bundle.Files[1].ContentBase64); string(text) != "reference a" {
		t.Fatalf("ref/a.md = %q", text)
	}
	// 保存修改后版本递增，清单与内容随之变化（不固定到配置版本）。
	manager.must(http.StatusOK, http.MethodPut, skillsPath(space)+"/"+first.ID+"/files",
		map[string]any{"version": 1, "changes": []map[string]any{{"path": "extra.md", "content": "extra"}}}, nil)
	env.deviceCall(executor, http.MethodGet, contextPath, nil).decode(t, &ctxBody)
	if ctxBody.Skills[1].Version != 2 || ctxBody.Skills[1].FileCount != 2 {
		t.Fatalf("after save = %+v", ctxBody.Skills[1])
	}

	// 授权与 Context 一致：别的电脑、别的组织的电脑 404；缺少 / 错误的 generation；租约过期。
	env.deviceCall(bystander, http.MethodGet, skillPath(second.ID), nil).expectStatus(t, http.StatusNotFound)
	env.deviceCall(foreign, http.MethodGet, skillPath(second.ID), nil).expectStatus(t, http.StatusNotFound)
	env.deviceCall(skillDevice{token: "bad"}, http.MethodGet, skillPath(second.ID), nil).expectStatus(t, http.StatusUnauthorized)
	env.deviceCall(executor, http.MethodGet, "/v1/device/organizations/"+env.org+"/runs/"+runID+"/skills/"+second.ID, nil).expectStatus(t, http.StatusBadRequest)
	env.deviceCall(executor, http.MethodGet, "/v1/device/organizations/"+env.org+"/runs/"+runID+"/skills/"+second.ID+"?generation=2", nil).expectStatus(t, http.StatusConflict)
	env.deviceCall(executor, http.MethodGet, skillPath("not-a-uuid"), nil).expectStatus(t, http.StatusNotFound)
	env.deviceCall(executor, http.MethodGet, skillPath(s3NewID(t)), nil).expectStatus(t, http.StatusNotFound)

	// 技能没有装配到该 Run 的 Agent（同空间的其他技能）→ 404；卸下后 → 404。
	loose := env.newSkill(manager, space, "loose-skill")
	env.deviceCall(executor, http.MethodGet, skillPath(loose.ID), nil).expectStatus(t, http.StatusNotFound)
	owner.must(http.StatusOK, http.MethodPut, "spaces/"+space+"/agents/"+agent.ID+"/skills", map[string]any{"skill_ids": []string{first.ID}}, nil)
	env.deviceCall(executor, http.MethodGet, skillPath(second.ID), nil).expectStatus(t, http.StatusNotFound)
	env.deviceCall(executor, http.MethodGet, skillPath(first.ID), nil).expectStatus(t, http.StatusOK)

	// A standalone install appears in a live device context. Its bundle can be fetched
	// only by this Agent's leased device; removal revokes download without touching space bindings.
	ownPath := "spaces/" + space + "/agents/" + agent.ID + "/market-skills/" + mBeta
	owner.must(204, "PUT", ownPath, nil, nil)
	otherAgent := env.insertAgent(space)
	owner.must(204, "PUT", "spaces/"+space+"/agents/"+otherAgent+"/market-skills/"+mBeta, nil, nil)
	var otherID string
	env.scalar(&otherID, "SELECT id::text FROM openfox.agent_market_skill_installs WHERE agent_id=$1", otherAgent)
	response = env.deviceCall(executor, "GET", contextPath, nil)
	response.expectStatus(t, 200)
	response.decode(t, &ctxBody)
	if len(ctxBody.Skills) != 2 || ctxBody.Skills[1].Name != "notes" {
		t.Fatalf("device loadout %+v", ctxBody.Skills)
	}
	ownID := ctxBody.Skills[1].ID
	response = env.deviceCall(executor, "GET", skillPath(ownID), nil)
	response.expectStatus(t, 200)
	response.decode(t, &bundle)
	if len(bundle.Files) != 1 || bundle.Files[0].Path != "SKILL.md" {
		t.Fatal("missing owned skill file")
	}
	content, err := base64.StdEncoding.DecodeString(bundle.Files[0].ContentBase64)
	if err != nil || !strings.Contains(string(content), "Take notes") {
		t.Fatal("wrong owned file contents")
	}
	env.deviceCall(executor, "GET", skillPath(otherID), nil).expectStatus(t, 404)
	env.deviceCall(bystander, "GET", skillPath(ownID), nil).expectStatus(t, 404)
	owner.must(200, "PUT", "spaces/"+space+"/agents/"+agent.ID+"/skills", map[string]any{"skill_ids": []string{first.ID}}, nil)
	env.deviceCall(executor, "GET", skillPath(ownID), nil).expectStatus(t, 404)
	// 租约过期 / Run 已结束：409，与 Context 一致。
	env.exec(`UPDATE openfox.runs SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, runID)
	env.deviceCall(executor, http.MethodGet, skillPath(first.ID), nil).expectStatus(t, http.StatusConflict)
	env.deviceCall(executor, http.MethodGet, contextPath, nil).expectStatus(t, http.StatusConflict)
}

// 存储键是随机 UUID：相同内容的文件各占一个对象，删除其中一个不会影响另一个。
func TestSkillStorageKeysAreIndependent(t *testing.T) {
	env := newS3Env(t)
	root := t.TempDir()
	withAvatarStorage(env, root)
	space := env.space.ID
	member := env.member()
	skill := env.newSkill(member, space, "same-content")
	path := skillsPath(space) + "/" + skill.ID + "/files"
	var saved skillDetailView
	member.must(http.StatusOK, http.MethodPut, path, map[string]any{"version": 1, "changes": []map[string]any{
		{"path": "same-a.md", "content": "identical content"}, {"path": "same-b.md", "content": "identical content"}}}, &saved)
	var keyA, keyB, digestA, digestB string
	env.scalar(&keyA, `SELECT file_key FROM openfox.skill_files WHERE organization_id = $1::uuid AND skill_id = $2::uuid AND path = 'same-a.md'`, env.org, skill.ID)
	env.scalar(&keyB, `SELECT file_key FROM openfox.skill_files WHERE organization_id = $1::uuid AND skill_id = $2::uuid AND path = 'same-b.md'`, env.org, skill.ID)
	env.scalar(&digestA, `SELECT sha256 FROM openfox.skill_files WHERE organization_id = $1::uuid AND skill_id = $2::uuid AND path = 'same-a.md'`, env.org, skill.ID)
	env.scalar(&digestB, `SELECT sha256 FROM openfox.skill_files WHERE organization_id = $1::uuid AND skill_id = $2::uuid AND path = 'same-b.md'`, env.org, skill.ID)
	if keyA == keyB || digestA != digestB || strings.Contains(keyA, digestA) {
		t.Fatalf("keys must be distinct random objects with equal digests: %s %s (%s %s)", keyA, keyB, digestA, digestB)
	}
	assertStoredFiles(t, env, root)

	// 删掉 same-a.md：same-b.md 仍能读到，存储里只剩它自己的对象。
	member.must(http.StatusOK, http.MethodPut, path, map[string]any{"version": 2, "changes": []map[string]any{{"path": "same-a.md", "delete": true}}}, &saved)
	if content, _ := env.skillFile(member, space, skill.ID, "same-b.md"); content != "identical content" {
		t.Fatalf("same-b.md after deleting same-a.md = %q", content)
	}
	if _, err := os.Stat(filepath.Join(root, keyA)); err == nil {
		t.Fatal("the deleted file's object must be removed")
	}
	assertStoredFiles(t, env, root)

	// 用同样的内容覆盖 same-b.md：写入新对象，旧对象被清理，内容可读。
	member.must(http.StatusOK, http.MethodPut, path, map[string]any{"version": 3, "changes": []map[string]any{{"path": "same-b.md", "content": "identical content"}}}, &saved)
	var rewrittenKey string
	env.scalar(&rewrittenKey, `SELECT file_key FROM openfox.skill_files WHERE organization_id = $1::uuid AND skill_id = $2::uuid AND path = 'same-b.md'`, env.org, skill.ID)
	if rewrittenKey == keyB {
		t.Fatal("rewriting a file must write a new object")
	}
	if content, _ := env.skillFile(member, space, skill.ID, "same-b.md"); content != "identical content" {
		t.Fatalf("same-b.md after rewrite = %q", content)
	}
	if _, err := os.Stat(filepath.Join(root, keyB)); err == nil {
		t.Fatal("the replaced object must be removed")
	}
	assertStoredFiles(t, env, root)
}
