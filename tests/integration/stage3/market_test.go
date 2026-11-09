package stage3

// 市场集成测试：导入工具（直接调用导入函数）、目录读取、添加到空间、更新、模板应用与权限。
// 市场目录是全平台共用的表，没有 RLS：每个用例开始时清空市场表并换一个新的存储目录，用例之间不共享数据。
// 样例数据在 internal/market/testdata/sample（3 个可导入技能、4 个应被跳过的技能、2 个模板）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/market"
)

const (
	mAlpha = "skillhub.alpha.pdf-tools"
	mBeta  = "skillhub.beta.notes"
	mGamma = "skillhub.gamma.csvtool"
)

// ---- 视图 ----

type mText map[string]string

type mSkillView struct {
	MarketSkillID string   `json:"market_skill_id"`
	Version       string   `json:"version"`
	Name          mText    `json:"name"`
	Description   mText    `json:"description"`
	Icon          *string  `json:"icon"`
	CategoryIDs   []string `json:"category_ids"`
	Tags          []mText  `json:"tags"`
	Publisher     struct {
		Name     string `json:"name"`
		Verified bool   `json:"verified"`
	} `json:"publisher"`
	Origin     string `json:"origin"`
	Featured   bool   `json:"featured"`
	Downloads  int64  `json:"downloads"`
	Stars      int64  `json:"stars"`
	FileCount  int    `json:"file_count"`
	TotalBytes int64  `json:"total_bytes"`
	Files      []struct {
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
		Mime      string `json:"mime"`
	} `json:"files"`
}

type mSkillPage struct {
	Items      []mSkillView `json:"items"`
	NextCursor *string      `json:"next_cursor"`
	Total      int          `json:"total"`
}

func (p mSkillPage) ids() []string {
	ids := make([]string, len(p.Items))
	for i, item := range p.Items {
		ids[i] = item.MarketSkillID
	}
	return ids
}

type mTemplateView struct {
	MarketTemplateID string         `json:"market_template_id"`
	Version          string         `json:"version"`
	Name             mText          `json:"name"`
	JobTitle         mText          `json:"job_title"`
	CategoryIDs      []string       `json:"category_ids"`
	Keywords         []string       `json:"keywords"`
	Avatar           map[string]any `json:"avatar"`
	SkillCount       int            `json:"skill_count"`
	Instructions     mText          `json:"instructions"`
	QuickPrompts     []mText        `json:"quick_prompts"`
	Skills           []struct {
		MarketSkillID string      `json:"market_skill_id"`
		Available     bool        `json:"available"`
		Skill         *mSkillView `json:"skill"`
	} `json:"skills"`
}

type mTemplatePage struct {
	Items      []mTemplateView `json:"items"`
	NextCursor *string         `json:"next_cursor"`
	Total      int             `json:"total"`
}

type mCatalogView struct {
	Source         string  `json:"source"`
	CatalogVersion string  `json:"catalog_version"`
	ImportedAt     *string `json:"imported_at"`
	Categories     []struct {
		ID            string `json:"id"`
		SkillCount    int    `json:"skill_count"`
		TemplateCount int    `json:"template_count"`
	} `json:"categories"`
	Packs []struct {
		ID      string `json:"id"`
		Members []struct {
			MarketSkillID string `json:"market_skill_id"`
			Available     bool   `json:"available"`
		} `json:"members"`
	} `json:"packs"`
	Counts struct {
		Skills    int `json:"skills"`
		Templates int `json:"templates"`
	} `json:"counts"`
}

// mSpaceSkill 是带市场字段的空间技能详情。
type mSpaceSkill struct {
	skillDetailView
	MarketSkillID       *string `json:"market_skill_id"`
	MarketVersion       *string `json:"market_version"`
	MarketLatestVersion *string `json:"market_latest_version"`
}

type mSpaceSkillList struct {
	Skills []struct {
		skillView
		MarketSkillID       *string `json:"market_skill_id"`
		MarketVersion       *string `json:"market_version"`
		MarketLatestVersion *string `json:"market_latest_version"`
	} `json:"skills"`
}

type mApplyResult struct {
	Items []struct {
		MarketSkillID string  `json:"market_skill_id"`
		Status        string  `json:"status"`
		Reason        string  `json:"reason"`
		SkillID       *string `json:"skill_id"`
	} `json:"items"`
}

func (r mApplyResult) summary() string {
	parts := make([]string, len(r.Items))
	for i, item := range r.Items {
		parts[i] = item.MarketSkillID + "=" + item.Status
		if item.Reason != "" {
			parts[i] += "(" + item.Reason + ")"
		}
	}
	return strings.Join(parts, " ")
}

// ---- 夹具 ----

// marketEnv 清空市场表、换新的存储目录，并准备一份可修改的样例数据副本。
type marketEnv struct {
	*s3Env
	root string
	data string
}

func newMarketEnv(t *testing.T) *marketEnv {
	t.Helper()
	env := newS3Env(t)
	env.exec(`TRUNCATE openfox.market_skills, openfox.market_templates, openfox.market_catalog CASCADE`)
	root := t.TempDir()
	withAvatarStorage(env, root)
	data := t.TempDir()
	copyTree(t, filepath.Join("..", "..", "..", "internal", "market", "testdata", "sample"), data)
	return &marketEnv{s3Env: env, root: root, data: data}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(from, path)
		target := filepath.Join(to, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// putSkill 在样例副本里新增或覆盖一个技能（同一格式：detail.json、files/<path>.json、skills-meta.json、selected.json）。
func (e *marketEnv) putSkill(id, version string, files map[string]string, categories []string, downloads int64) {
	e.t.Helper()
	nodes := []map[string]any{}
	for path, content := range files {
		sum := sha256.Sum256([]byte(content))
		node := map[string]any{"type": "file", "path": path, "size": len(content), "mime": "text/plain", "previewable": true, "sha256": hex.EncodeToString(sum[:])}
		nodes = append(nodes, node)
		file := map[string]any{"path": path, "size": len(content), "sha256": node["sha256"], "content": content}
		writeJSONFile(e.t, filepath.Join(e.data, "skills", id, "files", filepath.FromSlash(path)+".json"), file)
	}
	meta := map[string]any{
		"skillId": id, "version": version, "digest": "d-" + version, "name": map[string]string{"en": id},
		"description": map[string]string{"en": "description of " + id}, "featured": false, "categoryIds": categories,
		"tags": []map[string]string{{"en": "tag"}}, "stats": map[string]int64{"downloads": downloads, "installs": 0, "stars": 0},
		"publisher": map[string]any{"name": "Sample Org", "verified": false}, "origin": "skillhub", "source": map[string]string{"origin": "skillhub"},
	}
	writeJSONFile(e.t, filepath.Join(e.data, "skills", id, "detail.json"), map[string]any{"meta": meta, "tree": map[string]any{"nodes": nodes}})
	metas := map[string]any{}
	readJSONFile(e.t, filepath.Join(e.data, "skills-meta.json"), &metas)
	metas[id] = meta
	writeJSONFile(e.t, filepath.Join(e.data, "skills-meta.json"), metas)
	var selected []string
	readJSONFile(e.t, filepath.Join(e.data, "selected.json"), &selected)
	for _, existing := range selected {
		if existing == id {
			return
		}
	}
	writeJSONFile(e.t, filepath.Join(e.data, "selected.json"), append(selected, id))
}

// dropFromSample 从样例副本的 selected.json / templates.json 里去掉指定的技能与模板。
func (e *marketEnv) dropFromSample(skillIDs, templateIDs []string) {
	e.t.Helper()
	var selected []string
	readJSONFile(e.t, filepath.Join(e.data, "selected.json"), &selected)
	kept := []string{}
	for _, id := range selected {
		if !containsString(skillIDs, id) {
			kept = append(kept, id)
		}
	}
	writeJSONFile(e.t, filepath.Join(e.data, "selected.json"), kept)
	var templates []map[string]any
	readJSONFile(e.t, filepath.Join(e.data, "templates.json"), &templates)
	keptTemplates := []map[string]any{}
	for _, item := range templates {
		if !containsString(templateIDs, item["templateId"].(string)) {
			keptTemplates = append(keptTemplates, item)
		}
	}
	writeJSONFile(e.t, filepath.Join(e.data, "templates.json"), keptTemplates)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// importData 用独立连接调用导入函数（等同 openfox-server market import）。
func (e *marketEnv) importData(opts market.ImportOptions) (market.Report, error) {
	e.t.Helper()
	data, err := market.LoadDir(e.data)
	if err != nil {
		e.t.Fatal(err)
	}
	conn, err := pgx.Connect(e.ctx, os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if err != nil {
		e.t.Fatal(err)
	}
	defer conn.Close(context.Background())
	return market.Import(e.ctx, conn, artifacts.Local{Root: e.root}, data, opts)
}

func (e *marketEnv) mustImport(opts market.ImportOptions) market.Report {
	e.t.Helper()
	report, err := e.importData(opts)
	if err != nil {
		e.t.Fatal(err)
	}
	return report
}

func (e *marketEnv) marketPath(rest string) string { return "market/" + rest }

func (e *marketEnv) skills(client *s3Client, query string) mSkillPage {
	e.t.Helper()
	var page mSkillPage
	client.must(http.StatusOK, http.MethodGet, e.marketPath("skills")+query, nil, &page)
	return page
}

// countObjects 返回存储目录下某个前缀里的文件数。
func (e *marketEnv) countObjects(prefix ...string) int {
	count := 0
	_ = filepath.Walk(filepath.Join(append([]string{e.root}, prefix...)...), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			count++
		}
		return nil
	})
	return count
}

func (e *marketEnv) spaceSkills(client *s3Client, spaceID string) mSpaceSkillList {
	e.t.Helper()
	var list mSpaceSkillList
	client.must(http.StatusOK, http.MethodGet, skillsPath(spaceID), nil, &list)
	return list
}

func errorDetails(t *testing.T, response s3Response) map[string]any {
	t.Helper()
	var body struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	response.decode(t, &body)
	return body.Error.Details
}

// ---- 导入与读取 ----

func TestMarketImportAndRead(t *testing.T) {
	env := newMarketEnv(t)
	member := env.member()

	// 还没导入：目录为空，列表为空。
	var empty mCatalogView
	member.must(http.StatusOK, http.MethodGet, "market/catalog", nil, &empty)
	if empty.Source != "" || empty.ImportedAt != nil || len(empty.Categories) != 0 || len(empty.Packs) != 0 || empty.Counts.Skills != 0 {
		t.Fatalf("empty catalog = %+v", empty)
	}
	if page := env.skills(member, ""); len(page.Items) != 0 || page.Total != 0 || page.NextCursor != nil {
		t.Fatalf("empty list = %+v", page)
	}

	// 导入样例：3 个技能、2 个模板；4 个技能被跳过并带原因。
	dry := env.mustImport(market.ImportOptions{DryRun: true})
	if !dry.DryRun || dry.SkillsImported != 3 || env.count(`SELECT count(*) FROM openfox.market_skills`) != 0 {
		t.Fatalf("dry run = %+v", dry)
	}
	report := env.mustImport(market.ImportOptions{})
	if report.SkillsImported != 3 || report.SkillsUpdated != 0 || report.SkillsUnchanged != 0 || report.TemplatesImported != 2 || len(report.SkippedSkills) != 4 {
		t.Fatalf("report = %+v", report)
	}
	reasons := map[string]string{}
	for _, skip := range report.SkippedSkills {
		reasons[skip.ID] = skip.Reason
	}
	if reasons["skillhub.bad.missing"] != "FILE_CONTENT_MISSING" || reasons["skillhub.bad.digest"] != "FILE_DIGEST_MISMATCH" ||
		reasons["skillhub.bad.nomd"] != "NO_SKILL_MD" || reasons["skillhub.ghost.absent"] != "DETAIL_MISSING" {
		t.Fatalf("skip reasons = %v", reasons)
	}
	if env.count(`SELECT count(*) FROM openfox.market_skill_files`) != 6 || env.countObjects("market") != 6 {
		t.Fatalf("files = %d objects = %d", env.count(`SELECT count(*) FROM openfox.market_skill_files`), env.countObjects("market"))
	}

	// 幂等：重复导入不变（版本相同跳过，目录未变不改 imported_at）。
	var importedAt string
	env.scalar(&importedAt, `SELECT max(imported_at)::text FROM (SELECT imported_at FROM openfox.market_skills UNION ALL SELECT imported_at FROM openfox.market_catalog) x`)
	again := env.mustImport(market.ImportOptions{})
	if again.SkillsImported != 0 || again.SkillsUpdated != 0 || again.SkillsUnchanged != 3 || again.TemplatesUnchanged != 2 || env.countObjects("market") != 6 {
		t.Fatalf("second import = %+v objects %d", again, env.countObjects("market"))
	}
	var importedAfter string
	env.scalar(&importedAfter, `SELECT max(imported_at)::text FROM (SELECT imported_at FROM openfox.market_skills UNION ALL SELECT imported_at FROM openfox.market_catalog) x`)
	if importedAfter != importedAt {
		t.Fatalf("idempotent import changed imported_at: %s -> %s", importedAt, importedAfter)
	}

	// 目录：分类数量按实际导入重算；精选包里不在目录的技能标为不可用。
	var catalog mCatalogView
	member.must(http.StatusOK, http.MethodGet, "market/catalog", nil, &catalog)
	counts := map[string][2]int{}
	for _, category := range catalog.Categories {
		counts[category.ID] = [2]int{category.SkillCount, category.TemplateCount}
	}
	if catalog.Source != "builtin" || catalog.CatalogVersion != "2026-01-01.1" || catalog.ImportedAt == nil || catalog.Counts.Skills != 3 || catalog.Counts.Templates != 2 ||
		len(catalog.Categories) != 3 || counts["productivity"] != [2]int{2, 1} || counts["writing"] != [2]int{2, 1} || counts["empty"] != [2]int{0, 0} {
		t.Fatalf("catalog = %+v counts %v", catalog, counts)
	}
	if len(catalog.Packs) != 1 || len(catalog.Packs[0].Members) != 2 || !catalog.Packs[0].Members[0].Available || catalog.Packs[0].Members[1].Available {
		t.Fatalf("packs = %+v", catalog.Packs)
	}

	// 列表排序：默认精选在前再按下载量；下载量；名称（中文名优先）。
	if got := env.skills(member, "").ids(); strings.Join(got, ",") != mAlpha+","+mBeta+","+mGamma {
		t.Fatalf("default order = %v", got)
	}
	if got := env.skills(member, "?sort=downloads").ids(); strings.Join(got, ",") != mAlpha+","+mBeta+","+mGamma {
		t.Fatalf("downloads order = %v", got)
	}
	if got := env.skills(member, "?sort=name").ids(); strings.Join(got, ",") != mGamma+","+mBeta+","+mAlpha {
		t.Fatalf("name order = %v", got)
	}
	if got := env.skills(member, "?sort=newest").ids(); len(got) != 3 {
		t.Fatalf("newest = %v", got)
	}
	first := env.skills(member, "")
	if item := first.Items[0]; item.Version != "1.0.0" || !item.Featured || item.Downloads != 500 || item.Stars != 7 || item.Publisher.Name != "Sample Org" ||
		!item.Publisher.Verified || item.Origin != "skillhub" || item.FileCount != 3 || item.Name["zh-Hans"] != "PDF 工具" || item.Icon == nil ||
		len(item.Tags) != 2 || item.TotalBytes <= 0 {
		t.Fatalf("first item = %+v", item)
	}

	// 分类与搜索（不区分大小写；% 与 _ 按字面匹配）。
	if got := env.skills(member, "?category=writing").ids(); strings.Join(got, ",") != mBeta+","+mGamma {
		t.Fatalf("category = %v", got)
	}
	for query, want := range map[string]string{
		"?q=PDF": mAlpha, "?q=" + url.QueryEscape("工具"): mAlpha, "?q=quickly": mBeta, "?q=" + url.QueryEscape("100%"): mGamma,
		"?q=" + url.QueryEscape("%"): mGamma, "?q=" + url.QueryEscape("_f"): mGamma,
	} {
		if got := env.skills(member, query).ids(); len(got) != 1 || got[0] != want {
			t.Fatalf("query %s = %v, want %s", query, got, want)
		}
	}
	if page := env.skills(member, "?q=nothing-matches-this&category=writing"); len(page.Items) != 0 || page.Total != 0 || page.NextCursor != nil {
		t.Fatalf("no result page = %+v", page)
	}

	// 分页：每种排序逐页走完，顺序一致、不重复。
	for _, sort := range []string{"default", "downloads", "name", "newest"} {
		full := env.skills(member, "?sort="+sort).ids()
		var walked []string
		cursor := ""
		for pages := 0; pages < 5; pages++ {
			page := env.skills(member, "?limit=1&sort="+sort+cursor)
			if page.Total != 3 || len(page.Items) != 1 {
				t.Fatalf("%s page = %+v", sort, page)
			}
			walked = append(walked, page.Items[0].MarketSkillID)
			if page.NextCursor == nil {
				break
			}
			cursor = "&cursor=" + url.QueryEscape(*page.NextCursor)
		}
		if strings.Join(walked, ",") != strings.Join(full, ",") {
			t.Fatalf("%s walked %v, full %v", sort, walked, full)
		}
	}
	two := env.skills(member, "?limit=2")
	if len(two.Items) != 2 || two.NextCursor == nil {
		t.Fatalf("limit 2 = %+v", two)
	}
	if rest := env.skills(member, "?limit=2&cursor="+url.QueryEscape(*two.NextCursor)); len(rest.Items) != 1 || rest.NextCursor != nil {
		t.Fatalf("rest = %+v", rest)
	}
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "market/skills?cursor=garbage", nil)
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "market/skills?sort=downloads&cursor="+url.QueryEscape(*two.NextCursor), nil)
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "market/skills?limit=0", nil)
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "market/skills?limit=101", nil)
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "market/skills?sort=bogus", nil)

	// ids 批量：按请求顺序，忽略不存在的与其他筛选。
	if got := env.skills(member, "?ids="+mGamma+",nope,"+mAlpha+"&category=empty&limit=1").ids(); strings.Join(got, ",") != mGamma+","+mAlpha {
		t.Fatalf("ids = %v", got)
	}
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "market/skills?ids="+strings.Join(tooMany, ","), nil)

	// 详情：SKILL.md 置顶，其余按路径排序。
	var detail mSkillView
	member.must(http.StatusOK, http.MethodGet, "market/skills/"+mAlpha, nil, &detail)
	paths := []string{}
	for _, file := range detail.Files {
		paths = append(paths, file.Path)
	}
	if strings.Join(paths, ",") != "SKILL.md,data/blob.bin,scripts/run.py" || detail.Files[0].Mime != "text/plain" || detail.Files[2].SizeBytes != int64(len("print('hi')\n")) {
		t.Fatalf("detail files = %+v", detail.Files)
	}
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "market/skills/skillhub.nothing.here", nil)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "market/skills/skillhub.bad.missing", nil)

	// 文件：文本、二进制、路径不存在 / 不合规。
	fileURL := func(id, path string) string { return "market/skills/" + id + "/file?path=" + url.QueryEscape(path) }
	var file struct {
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
		Mime      string `json:"mime"`
		Binary    bool   `json:"binary"`
		Truncated bool   `json:"truncated"`
		Content   string `json:"content"`
	}
	member.must(http.StatusOK, http.MethodGet, fileURL(mAlpha, "scripts/run.py"), nil, &file)
	if file.Binary || file.Truncated || file.Content != "print('hi')\n" || file.Mime != "text/plain" || file.SizeBytes != 12 {
		t.Fatalf("text file = %+v", file)
	}
	member.must(http.StatusOK, http.MethodGet, fileURL(mAlpha, "data/blob.bin"), nil, &file)
	if !file.Binary || file.Content != "" || file.SizeBytes != int64(len("\x00\x01binary")) {
		t.Fatalf("binary file = %+v", file)
	}
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, fileURL(mAlpha, "nope.txt"), nil)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, fileURL(mAlpha, "../x"), nil)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, fileURL("skillhub.nothing.here", "SKILL.md"), nil)

	// 下载原文：attachment、nosniff、字节一致。
	response := member.do(http.MethodGet, "market/skills/"+mAlpha+"/download?path="+url.QueryEscape("data/blob.bin"), nil)
	if response.Status != http.StatusOK || string(response.Body) != "\x00\x01binary" || response.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment") || !strings.Contains(response.Header.Get("Content-Disposition"), "blob.bin") ||
		response.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("download = %d %q %v", response.Status, response.Body, response.Header)
	}
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "market/skills/"+mAlpha+"/download?path=missing", nil)

	// 模板：按 position 排序、搜索、分类、分页；详情带指令与技能可用性。
	var templates mTemplatePage
	member.must(http.StatusOK, http.MethodGet, "market/templates", nil, &templates)
	if templates.Total != 2 || len(templates.Items) != 2 || templates.Items[0].MarketTemplateID != "skillhub.tpl-writer" || templates.Items[0].SkillCount != 4 ||
		templates.Items[0].Name["zh-Hans"] != "写作助手" || templates.Items[0].Avatar["url"] == nil || templates.Items[1].SkillCount != 0 {
		t.Fatalf("templates = %+v", templates)
	}
	member.must(http.StatusOK, http.MethodGet, "market/templates?q="+url.QueryEscape("写手"), nil, &templates)
	if len(templates.Items) != 2 {
		t.Fatalf("template search = %+v", templates)
	}
	member.must(http.StatusOK, http.MethodGet, "market/templates?category=productivity", nil, &templates)
	if len(templates.Items) != 1 || templates.Items[0].MarketTemplateID != "skillhub.tpl-second" {
		t.Fatalf("template category = %+v", templates)
	}
	member.must(http.StatusOK, http.MethodGet, "market/templates?limit=1", nil, &templates)
	if len(templates.Items) != 1 || templates.NextCursor == nil || templates.Total != 2 {
		t.Fatalf("template page = %+v", templates)
	}
	member.must(http.StatusOK, http.MethodGet, "market/templates?limit=1&cursor="+url.QueryEscape(*templates.NextCursor), nil, &templates)
	if len(templates.Items) != 1 || templates.NextCursor != nil || templates.Items[0].MarketTemplateID != "skillhub.tpl-second" {
		t.Fatalf("template page 2 = %+v", templates)
	}
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "market/templates?cursor=garbage", nil)
	var template mTemplateView
	member.must(http.StatusOK, http.MethodGet, "market/templates/skillhub.tpl-writer", nil, &template)
	if template.Instructions["en"] != "You are a writer." || template.Instructions["zh-Hans"] != "你是写手。" || len(template.QuickPrompts) != 1 ||
		template.QuickPrompts[0]["en"] != "Draft a post" || len(template.Skills) != 4 {
		t.Fatalf("template detail = %+v", template)
	}
	available := []bool{}
	for _, ref := range template.Skills {
		available = append(available, ref.Available)
		if ref.Available != (ref.Skill != nil) {
			t.Fatalf("ref = %+v", ref)
		}
	}
	if template.Skills[2].MarketSkillID != "skillhub.not.here" || available[0] != true || available[1] != true || available[2] != false || available[3] != true {
		t.Fatalf("availability = %v %+v", available, template.Skills)
	}
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "market/templates/skillhub.nothing", nil)

	// 跨租户与非成员：404；未登录：401。
	outsider := env.outsider()
	for _, path := range []string{"market/catalog", "market/skills", "market/skills/" + mAlpha, "market/templates", "market/templates/skillhub.tpl-writer",
		fileURL(mAlpha, "SKILL.md"), "market/skills/" + mAlpha + "/download?path=SKILL.md"} {
		outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path, nil)
		member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "/v1/organizations/"+env.otherOrg+"/"+path, nil)
	}
	anonymous, err := http.Get(env.server.URL + "/v1/organizations/" + env.org + "/market/catalog")
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", anonymous.StatusCode)
	}
}

func TestMarketFilePreviewLimits(t *testing.T) {
	env := newMarketEnv(t)
	member := env.member()
	big := strings.Repeat("0123456789abcdef", 1<<16) + "tail" // 1 MB + 4 字节
	exact := strings.Repeat("x", 1<<20)
	// 8 KB 之后才出现的 NUL 不影响文本判断；前 8 KB 内有 NUL 才是二进制。
	late := strings.Repeat("a", 9000) + "\x00end"
	early := "ab\x00" + strings.Repeat("a", 100)
	env.putSkill("skillhub.size.test", "1.0.0", map[string]string{
		"SKILL.md": "---\nname: size-test\ndescription: sizes\n---\n", "big.txt": big, "exact.txt": exact, "late.txt": late, "early.bin": early, "empty.txt": "",
	}, []string{"writing"}, 1)
	env.dropFromSample([]string{"skillhub.bad.missing", "skillhub.bad.digest"}, nil)
	if report := env.mustImport(market.ImportOptions{}); report.SkillsImported != 4 {
		t.Fatalf("report = %+v", report)
	}
	read := func(path string) (binary, truncated bool, content string, size int64) {
		var file struct {
			SizeBytes int64  `json:"size_bytes"`
			Binary    bool   `json:"binary"`
			Truncated bool   `json:"truncated"`
			Content   string `json:"content"`
		}
		member.must(http.StatusOK, http.MethodGet, "market/skills/skillhub.size.test/file?path="+url.QueryEscape(path), nil, &file)
		return file.Binary, file.Truncated, file.Content, file.SizeBytes
	}
	if binary, truncated, content, size := read("big.txt"); binary || !truncated || len(content) != 1<<20 || content != big[:1<<20] || size != int64(len(big)) {
		t.Fatalf("big: binary=%v truncated=%v len=%d size=%d", binary, truncated, len(content), size)
	}
	if binary, truncated, content, _ := read("exact.txt"); binary || truncated || len(content) != 1<<20 {
		t.Fatalf("exact: binary=%v truncated=%v len=%d", binary, truncated, len(content))
	}
	if binary, _, content, _ := read("late.txt"); binary || len(content) != len(late) {
		t.Fatalf("late NUL must stay text: binary=%v", binary)
	}
	if binary, _, content, _ := read("early.bin"); !binary || content != "" {
		t.Fatalf("early NUL must be binary: binary=%v", binary)
	}
	if binary, truncated, content, size := read("empty.txt"); binary || truncated || content != "" || size != 0 {
		t.Fatalf("empty file: %v %v %q %d", binary, truncated, content, size)
	}
	// 下载给出完整原文，不截断。
	response := member.do(http.MethodGet, "market/skills/skillhub.size.test/download?path=big.txt", nil)
	if response.Status != http.StatusOK || string(response.Body) != big {
		t.Fatalf("download = %d len %d", response.Status, len(response.Body))
	}
}

func TestMarketAppRoleCannotWrite(t *testing.T) {
	env := newMarketEnv(t)
	env.mustImport(market.ImportOptions{})
	statements := []string{
		`INSERT INTO openfox.market_skills (skill_id, version, name) VALUES ('skillhub.x.y', '1', '{}')`,
		`UPDATE openfox.market_skills SET downloads = 0`,
		`DELETE FROM openfox.market_skills`,
		`INSERT INTO openfox.market_templates (template_id, version, name) VALUES ('skillhub.x', '1', '{}')`,
		`DELETE FROM openfox.market_skill_files`,
		`UPDATE openfox.market_catalog SET source = 'x'`,
		`TRUNCATE openfox.market_templates`,
	}
	for _, statement := range statements {
		tx, err := env.superuser.Begin(env.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(env.ctx, `SET LOCAL ROLE openfox_app`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(env.ctx, statement); err == nil {
			t.Fatalf("openfox_app must not run %q", statement)
		}
		_ = tx.Rollback(env.ctx)
	}
	// 读取可以。
	tx, err := env.superuser.Begin(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(env.ctx)
	if _, err := tx.Exec(env.ctx, `SET LOCAL ROLE openfox_app`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(env.ctx, `SELECT count(*) FROM openfox.market_skills`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("app role read: %d %v", count, err)
	}
}

// ---- 添加到空间 ----

func TestMarketAddToSpace(t *testing.T) {
	env := newMarketEnv(t)
	env.mustImport(market.ImportOptions{})
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	member, viewer := env.member(), env.other()
	base := skillsPath(space) + "/market"

	var added mSpaceSkill
	member.must(http.StatusCreated, http.MethodPost, base, map[string]any{"market_skill_id": mAlpha}, &added)
	if added.Name != "pdf-tools" || added.Description != "Work with PDF files" || added.SourceKind != "market" || added.SourceLabel != "Sample Org" ||
		added.MarketSkillID == nil || *added.MarketSkillID != mAlpha || added.MarketVersion == nil || *added.MarketVersion != "1.0.0" ||
		added.MarketLatestVersion == nil || *added.MarketLatestVersion != "1.0.0" || added.FileCount != 3 || !added.CanEdit || added.Version != 1 {
		t.Fatalf("added = %+v", added)
	}
	if got := strings.Join(added.paths(), ","); got != "SKILL.md,data/blob.bin,scripts/run.py" {
		t.Fatalf("files = %s", got)
	}
	if content, binary := env.skillFile(member, space, added.ID, "scripts/run.py"); binary || content != "print('hi')\n" {
		t.Fatalf("copied file = %q %v", content, binary)
	}
	if _, binary := env.skillFile(member, space, added.ID, "data/blob.bin"); !binary {
		t.Fatal("binary file must stay binary")
	}
	// 空间技能有自己的存储对象，删除后市场文件仍在。
	if env.countObjects("skills", env.org, added.ID) != 3 || env.countObjects("market") != 6 {
		t.Fatalf("objects skill=%d market=%d", env.countObjects("skills", env.org, added.ID), env.countObjects("market"))
	}

	// 名称取自 frontmatter；不合规时由市场 ID 末段规范化、描述取市场目录。
	var notes mSpaceSkill
	member.must(http.StatusCreated, http.MethodPost, base, map[string]any{"market_skill_id": mBeta}, &notes)
	if notes.Name != "notes" || notes.Description != "Take notes quickly" || notes.SourceKind != "market" {
		t.Fatalf("notes = %+v", notes)
	}

	// 重复添加：409 SKILL_MARKET_EXISTS，details 带已有技能。
	dup := member.expectError(http.StatusConflict, "SKILL_MARKET_EXISTS", http.MethodPost, base, map[string]any{"market_skill_id": mAlpha})
	if details := errorDetails(t, dup); details["skill_id"] != added.ID {
		t.Fatalf("details = %v", details)
	}
	// 重名：409 SKILL_NAME_TAKEN，且不留下存储对象。
	env.newSkill(member, space, "csvtool")
	before := env.countObjects("skills", env.org)
	member.expectError(http.StatusConflict, "SKILL_NAME_TAKEN", http.MethodPost, base, map[string]any{"market_skill_id": mGamma})
	if env.countObjects("skills", env.org) != before {
		t.Fatal("a failed add must clean up its storage objects")
	}
	// 市场里没有 / 请求不合法。
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, base, map[string]any{"market_skill_id": "skillhub.nothing.here"})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, map[string]any{"market_skill_id": "bad id/"})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base, map[string]any{})
	// 只读成员：403。
	viewer.expectError(http.StatusForbidden, "", http.MethodPost, base, map[string]any{"market_skill_id": mGamma})
	// 空间技能列表带市场字段。
	list := env.spaceSkills(member, space)
	found := false
	for _, skill := range list.Skills {
		if skill.Name == "pdf-tools" {
			found = skill.MarketVersion != nil && skill.MarketLatestVersion != nil && *skill.MarketLatestVersion == "1.0.0"
		}
		if skill.Name == "csvtool" && (skill.MarketSkillID != nil || skill.MarketLatestVersion != nil) {
			t.Fatalf("manual skill must not carry market fields: %+v", skill)
		}
	}
	if !found {
		t.Fatalf("list = %+v", list)
	}
	// 添加后的技能与普通空间技能一样可以编辑与删除；删除不影响市场文件。
	member.must(http.StatusNoContent, http.MethodDelete, skillsPath(space)+"/"+added.ID+"?version=1", nil, nil)
	if env.countObjects("market") != 6 {
		t.Fatal("deleting a space skill must not touch market files")
	}
	var again mSpaceSkill
	member.must(http.StatusCreated, http.MethodPost, base, map[string]any{"market_skill_id": mAlpha}, &again)

	// 停用空间：409 SPACE_RETIRED。
	env.exec(`UPDATE openfox.spaces SET status = 'ARCHIVED' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space)
	member.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, base, map[string]any{"market_skill_id": mGamma})
}

// ---- 更新 ----

func TestMarketUpdateSkill(t *testing.T) {
	env := newMarketEnv(t)
	env.mustImport(market.ImportOptions{})
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	env.addSpaceMember(space, s3AdminID, "MEMBER")
	member, manager, viewer := env.member(), env.manager(), env.other()
	var added mSpaceSkill
	member.must(http.StatusCreated, http.MethodPost, skillsPath(space)+"/market", map[string]any{"market_skill_id": mAlpha}, &added)
	update := skillsPath(space) + "/" + added.ID + "/market-update"

	// 已是最新。
	member.expectError(http.StatusConflict, "SKILL_UP_TO_DATE", http.MethodPost, update, map[string]any{"version": 1})

	// 市场发布新版本：改描述、删一个文件、新增一个文件。
	env.putSkill(mAlpha, "1.1.0", map[string]string{
		"SKILL.md":       "---\nname: renamed-upstream\ndescription: Newer PDF helper\n---\n\nnew\n",
		"scripts/run.py": "print('v2')\n", "docs/new.md": "# new\n",
	}, []string{"productivity"}, 600)
	report := env.mustImport(market.ImportOptions{})
	if report.SkillsUpdated != 1 || report.SkillsUnchanged != 2 || report.SkillsImported != 0 {
		t.Fatalf("report = %+v", report)
	}
	// 旧存储对象在提交后被删除，只剩新版本的 3 个文件。
	if env.countObjects("market", mAlpha) != 3 || env.count(`SELECT count(*) FROM openfox.market_skill_files WHERE skill_id = $1`, mAlpha) != 3 {
		t.Fatalf("market objects = %d", env.countObjects("market", mAlpha))
	}
	list := env.spaceSkills(member, space)
	if len(list.Skills) != 1 || list.Skills[0].MarketVersion == nil || *list.Skills[0].MarketVersion != "1.0.0" ||
		list.Skills[0].MarketLatestVersion == nil || *list.Skills[0].MarketLatestVersion != "1.1.0" {
		t.Fatalf("list = %+v", list)
	}

	// 权限：只读成员 403；非创建者的空间成员（manager 是空间 OWNER 但不是创建者，也不是组织管理员）403。
	viewer.expectError(http.StatusForbidden, "", http.MethodPost, update, map[string]any{"version": 1})
	manager.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, update, map[string]any{"version": 1})
	member.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, update, map[string]any{"version": 7})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, update, map[string]any{"version": 0})

	// 创建者更新：文件整体替换、名称不变、描述取新版本、版本号 +1。
	var updated mSpaceSkill
	member.must(http.StatusOK, http.MethodPost, update, map[string]any{"version": 1}, &updated)
	if updated.Name != "pdf-tools" || updated.Description != "Newer PDF helper" || updated.Version != 2 || updated.MarketVersion == nil || *updated.MarketVersion != "1.1.0" ||
		updated.MarketLatestVersion == nil || *updated.MarketLatestVersion != "1.1.0" || updated.SourceKind != "market" {
		t.Fatalf("updated = %+v", updated)
	}
	if got := strings.Join(updated.paths(), ","); got != "SKILL.md,docs/new.md,scripts/run.py" {
		t.Fatalf("files = %s", got)
	}
	if content, _ := env.skillFile(member, space, added.ID, "scripts/run.py"); content != "print('v2')\n" {
		t.Fatalf("run.py = %q", content)
	}
	if env.countObjects("skills", env.org, added.ID) != 3 {
		t.Fatalf("old skill objects must be removed, have %d", env.countObjects("skills", env.org, added.ID))
	}
	member.expectError(http.StatusConflict, "SKILL_UP_TO_DATE", http.MethodPost, update, map[string]any{"version": 2})

	// 组织管理员也可以更新（不是创建者）：再发一个版本。
	env.putSkill(mAlpha, "1.2.0", map[string]string{"SKILL.md": "---\nname: x\ndescription: Third\n---\n"}, []string{"productivity"}, 700)
	env.mustImport(market.ImportOptions{})
	var byAdmin mSpaceSkill
	env.admin().must(http.StatusOK, http.MethodPost, update, map[string]any{"version": 2}, &byAdmin)
	if byAdmin.Description != "Third" || byAdmin.FileCount != 1 || byAdmin.Version != 3 {
		t.Fatalf("by admin = %+v", byAdmin)
	}

	// 手动创建的技能不是市场技能：400。
	manual := env.newSkill(member, space, "manual-one")
	member.expectError(http.StatusBadRequest, "SKILL_NOT_FROM_MARKET", http.MethodPost, skillsPath(space)+"/"+manual.ID+"/market-update", map[string]any{"version": 1})
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, skillsPath(space)+"/"+s3NewID(t)+"/market-update", map[string]any{"version": 1})

	// 市场撤下该技能（--prune）：market_latest_version 为 null，更新 404。
	env.dropFromSample([]string{mAlpha}, nil)
	env.mustImport(market.ImportOptions{Prune: true})
	list = env.spaceSkills(member, space)
	for _, skill := range list.Skills {
		if skill.Name == "pdf-tools" && (skill.MarketLatestVersion != nil || skill.MarketVersion == nil) {
			t.Fatalf("after prune = %+v", skill)
		}
	}
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, update, map[string]any{"version": 3})
}

// ---- 模板应用 ----

func TestMarketApplyTemplate(t *testing.T) {
	env := newMarketEnv(t)
	env.mustImport(market.ImportOptions{})
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	member, manager, viewer := env.member(), env.manager(), env.other()
	agent := env.insertAgent(space)
	apply := "spaces/" + space + "/agents/" + agent + "/market-template"
	agentSkills := func() []string {
		var assigned struct {
			Skills []skillView `json:"skills"`
		}
		manager.must(http.StatusOK, http.MethodGet, "spaces/"+space+"/agents/"+agent+"/skills", nil, &assigned)
		names := []string{}
		for _, skill := range assigned.Skills {
			names = append(names, skill.Name)
		}
		return names
	}

	// 准备：notes 已在空间里（reused）、同名的手动技能 csvtool 占住 gamma 的名称（skipped）。
	var notes mSpaceSkill
	member.must(http.StatusCreated, http.MethodPost, skillsPath(space)+"/market", map[string]any{"market_skill_id": mBeta}, &notes)
	env.newSkill(member, space, "csvtool")

	body := map[string]any{"market_template_id": "skillhub.tpl-writer"}
	// 权限：只读成员 403；不能编辑该 Agent 的普通成员 403；模板 / Agent 不存在 404。
	viewer.expectError(http.StatusForbidden, "", http.MethodPost, apply, body)
	member.expectError(http.StatusForbidden, "", http.MethodPost, apply, body)
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, apply, map[string]any{"market_template_id": "skillhub.nothing"})
	manager.expectError(http.StatusNotFound, "", http.MethodPost, "spaces/"+space+"/agents/"+s3NewID(t)+"/market-template", body)
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, apply, map[string]any{})

	var result mApplyResult
	manager.must(http.StatusOK, http.MethodPost, apply, body, &result)
	want := mAlpha + "=added " + mBeta + "=reused skillhub.not.here=missing " + mGamma + "=skipped(SKILL_NAME_TAKEN)"
	if result.summary() != want {
		t.Fatalf("result = %s\nwant   = %s", result.summary(), want)
	}
	if result.Items[0].SkillID == nil || result.Items[1].SkillID == nil || *result.Items[1].SkillID != notes.ID || result.Items[2].SkillID != nil || result.Items[3].SkillID != nil {
		t.Fatalf("skill ids = %+v", result.Items)
	}
	if got := strings.Join(agentSkills(), ","); got != "pdf-tools,notes" {
		t.Fatalf("agent skills = %s", got)
	}
	created := env.count(`SELECT count(*) FROM openfox.skills WHERE organization_id = $1::uuid AND space_id = $2::uuid AND market_skill_id = $3`, env.org, space, mAlpha)
	if created != 1 {
		t.Fatalf("alpha copies = %d", created)
	}

	// 再次应用：已装配的不重复装配，不重复添加。
	manager.must(http.StatusOK, http.MethodPost, apply, body, &result)
	want = mAlpha + "=attached " + mBeta + "=attached skillhub.not.here=missing " + mGamma + "=skipped(SKILL_NAME_TAKEN)"
	if result.summary() != want || strings.Join(agentSkills(), ",") != "pdf-tools,notes" {
		t.Fatalf("second result = %s agent = %v", result.summary(), agentSkills())
	}
	if env.count(`SELECT count(*) FROM openfox.skills WHERE organization_id = $1::uuid AND space_id = $2::uuid AND market_skill_id = $3`, env.org, space, mAlpha) != 1 {
		t.Fatal("applying twice must not add a second copy")
	}
	// 没有引用的模板：空结果。
	manager.must(http.StatusOK, http.MethodPost, apply, map[string]any{"market_template_id": "skillhub.tpl-second"}, &result)
	if len(result.Items) != 0 {
		t.Fatalf("empty template result = %+v", result)
	}

	// Agent 装配已满 20 个：其余 skipped（AGENT_SKILL_LIMIT），不新增空间技能。
	otherSpace := env.createSpace("shared")
	env.addSpaceMember(otherSpace.ID, s3ManagerID, "OWNER")
	fullAgent := env.insertAgent(otherSpace.ID)
	ids := []string{}
	for i := 0; i < 20; i++ {
		ids = append(ids, env.newSkill(manager, otherSpace.ID, "filler-"+strings.Repeat("a", i+1)).ID)
	}
	manager.must(http.StatusOK, http.MethodPut, "spaces/"+otherSpace.ID+"/agents/"+fullAgent+"/skills", map[string]any{"skill_ids": ids}, nil)
	manager.must(http.StatusOK, http.MethodPost, "spaces/"+otherSpace.ID+"/agents/"+fullAgent+"/market-template", body, &result)
	want = mAlpha + "=skipped(AGENT_SKILL_LIMIT) " + mBeta + "=skipped(AGENT_SKILL_LIMIT) skillhub.not.here=missing " + mGamma + "=skipped(AGENT_SKILL_LIMIT)"
	if result.summary() != want {
		t.Fatalf("full agent result = %s", result.summary())
	}
	if env.count(`SELECT count(*) FROM openfox.skills WHERE organization_id = $1::uuid AND space_id = $2::uuid AND market_skill_id IS NOT NULL`, env.org, otherSpace.ID) != 0 {
		t.Fatal("a full agent must not cause skills to be added")
	}

	// 停用空间：409。
	env.exec(`UPDATE openfox.spaces SET status = 'ARCHIVED' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space)
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, apply, body)
}

// ---- 导入工具的生命周期 ----

func TestMarketImportLifecycle(t *testing.T) {
	env := newMarketEnv(t)
	env.mustImport(market.ImportOptions{})
	member := env.member()
	if env.countObjects("market") != 6 {
		t.Fatalf("objects = %d", env.countObjects("market"))
	}

	// 新版本覆盖：文件清单整体替换，旧存储对象被删，下载量等元数据更新。
	env.putSkill(mGamma, "1.0.1", map[string]string{"SKILL.md": "---\nname: csvtool\ndescription: Newer\n---\n"}, []string{"writing"}, 999)
	report := env.mustImport(market.ImportOptions{})
	if report.SkillsUpdated != 1 || report.SkillsUnchanged != 2 || env.countObjects("market", mGamma) != 1 || env.countObjects("market") != 5 {
		t.Fatalf("report = %+v objects %d", report, env.countObjects("market"))
	}
	if got := env.skills(member, "?sort=downloads").ids(); got[0] != mGamma {
		t.Fatalf("downloads order after update = %v", got)
	}

	// 数据里没有的技能 / 模板默认保留；--prune 才删除（含存储对象），并重算分类数量。
	env.dropFromSample([]string{mBeta}, []string{"skillhub.tpl-second"})
	kept := env.mustImport(market.ImportOptions{})
	if kept.SkillsDeleted != 0 || kept.TemplatesDeleted != 0 || env.count(`SELECT count(*) FROM openfox.market_skills`) != 3 {
		t.Fatalf("without prune = %+v", kept)
	}
	dry := env.mustImport(market.ImportOptions{Prune: true, DryRun: true})
	if dry.SkillsDeleted != 1 || dry.TemplatesDeleted != 1 || env.count(`SELECT count(*) FROM openfox.market_skills`) != 3 {
		t.Fatalf("dry prune = %+v", dry)
	}
	pruned := env.mustImport(market.ImportOptions{Prune: true})
	if pruned.SkillsDeleted != 1 || pruned.TemplatesDeleted != 1 || env.count(`SELECT count(*) FROM openfox.market_skills`) != 2 ||
		env.count(`SELECT count(*) FROM openfox.market_templates`) != 1 || env.countObjects("market", mBeta) != 0 {
		t.Fatalf("pruned = %+v objects %d", pruned, env.countObjects("market", mBeta))
	}
	var catalog mCatalogView
	member.must(http.StatusOK, http.MethodGet, "market/catalog", nil, &catalog)
	for _, category := range catalog.Categories {
		if category.ID == "productivity" && (category.SkillCount != 1 || category.TemplateCount != 0) {
			t.Fatalf("productivity = %+v", category)
		}
	}
	if catalog.Counts.Skills != 2 || catalog.Counts.Templates != 1 {
		t.Fatalf("counts = %+v", catalog.Counts)
	}

	// 数据为空时拒绝 --prune，避免清空目录。
	env.dropFromSample([]string{mAlpha, mGamma, "skillhub.bad.missing", "skillhub.bad.digest", "skillhub.bad.nomd", "skillhub.ghost.absent"}, nil)
	if _, err := env.importData(market.ImportOptions{Prune: true}); err == nil {
		t.Fatal("prune with no skills must be refused")
	}
	if env.count(`SELECT count(*) FROM openfox.market_skills`) != 2 {
		t.Fatal("a refused prune must not delete anything")
	}

	// 存储不可写时整个导入失败，不留下半截数据。
	broken := t.TempDir()
	blocker := filepath.Join(broken, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.putSkill("skillhub.fresh.one", "1.0.0", map[string]string{"SKILL.md": "---\nname: fresh\ndescription: d\n---\n"}, []string{"writing"}, 1)
	data, err := market.LoadDir(env.data)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(env.ctx, os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := market.Import(env.ctx, conn, artifacts.Local{Root: blocker}, data, market.ImportOptions{}); err == nil {
		t.Fatal("import with an unwritable storage root must fail")
	}
	if env.count(`SELECT count(*) FROM openfox.market_skills WHERE skill_id = 'skillhub.fresh.one'`) != 0 {
		t.Fatal("a failed import must not write the database")
	}
}

func TestMarketAgentInstall(t *testing.T) {
	env := newMarketEnv(t)
	env.mustImport(market.ImportOptions{})
	space := env.space.ID
	a, b := env.insertAgent(space), env.insertAgent(space)
	env.exec("UPDATE openfox.agents SET owner_user_id=$2::uuid, name=$3 WHERE id=$1::uuid", a, s3MemberID, "Market QA agent A")
	env.exec("UPDATE openfox.agents SET name=$2 WHERE id=$1::uuid", b, "Market QA agent B")
	env.addSpaceMember(space, s3AdminID, "MEMBER")
	member, admin := env.member(), env.admin()
	route := func(id, skill string) string { return "spaces/" + space + "/agents/" + id + "/market-skills/" + skill }
	var list struct {
		Items      []struct{ ID string }
		NextCursor string `json:"next_cursor"`
	}
	targets := func(c *s3Client, q string) []string {
		c.must(200, "GET", "market/agent-targets?q="+url.QueryEscape(q), nil, &list)
		ids := []string{}
		for _, x := range list.Items {
			ids = append(ids, x.ID)
		}
		return ids
	}
	if got := targets(member, "mArKeT qA"); len(got) != 1 || got[0] != a {
		t.Fatalf("member targets: %v", got)
	}
	if got := targets(admin, env.space.Name); !containsString(got, a) || !containsString(got, b) {
		t.Fatalf("admin targets: %v", got)
	}
	env.manager().expectError(403, "", "PUT", route(b, mAlpha), nil)
	member.expectError(403, "", "PUT", route(b, mAlpha), nil)
	member.expectError(404, "NOT_FOUND", "PUT", route(a, "missing.skill"), nil)
	env.exec("UPDATE openfox.agents SET visibility='PRIVATE' WHERE id=$1::uuid", a)
	if got := targets(admin, "Market QA agent A"); len(got) != 0 {
		t.Fatalf("private exposed: %v", got)
	}
	admin.expectError(404, "", "PUT", route(a, mAlpha), nil)
	if res := env.outsider().do("GET", "market/agent-targets", nil); res.Status != 404 {
		t.Fatalf("other org: %d", res.Status)
	}
	// Concurrent installs on the same and different Agents are independent and idempotent.
	member.must(204, "PUT", route(a, mBeta), nil, nil)
	done := make(chan s3Response, 3)
	go func() { done <- member.do("PUT", route(a, mAlpha), nil) }()
	go func() { done <- member.do("PUT", route(a, mAlpha), nil) }()
	go func() { done <- admin.do("PUT", route(b, mAlpha), nil) }()
	for range 3 {
		if r := <-done; r.Status != 204 {
			t.Fatalf("parallel install: %d %s", r.Status, r.Body)
		}
	}
	member.must(204, "PUT", route(a, mAlpha), nil, nil)
	if n := env.count("SELECT count(*) FROM openfox.skills WHERE space_id=$1 AND market_skill_id=$2", space, mAlpha); n != 0 {
		t.Fatalf("copies: %d", n)
	}
	var attached struct{ Skills []skillView }
	member.must(200, "GET", "spaces/"+space+"/agents/"+a+"/skills", nil, &attached)
	if len(attached.Skills) != 2 || attached.Skills[0].Name != "notes" || attached.Skills[1].Name != "pdf-tools" {
		t.Fatalf("unexpected skills: %+v", attached.Skills)
	}
	admin.must(200, "GET", "spaces/"+space+"/agents/"+b+"/skills", nil, &attached)
	if len(attached.Skills) != 1 || attached.Skills[0].Name != "pdf-tools" {
		t.Fatalf("second agent: %+v", attached.Skills)
	}
	bid := attached.Skills[0].ID
	var aid string
	env.scalar(&aid, `SELECT id::text FROM openfox.agent_market_skill_installs WHERE agent_id=$1 AND market_skill_id=$2`, a, mAlpha)
	if aid == bid {
		t.Fatal("shared install ID")
	}
	var akey, bkey string
	for id, out := range map[string]*string{aid: &akey, bid: &bkey} {
		env.scalar(out, "SELECT files->0->>'Key' FROM openfox.agent_market_skill_installs WHERE id=$1", id)
	}
	if akey == bkey || akey == "" || bkey == "" {
		t.Fatal("shared or missing file key")
	}
	// Guessing a private install ID does not open it in the space library or attach it elsewhere.
	member.expectError(404, "NOT_FOUND", "GET", skillsPath(space)+"/"+aid, nil)
	member.expectError(404, "NOT_FOUND", "GET", skillsPath(space)+"/"+aid+"/files?path=SKILL.md", nil)
	admin.expectError(400, "INVALID_REQUEST", "PUT", "spaces/"+space+"/agents/"+b+"/skills", map[string]any{"skill_ids": []string{aid}})
	// A same-name space skill is allowed. Only an actual same-name binding on this Agent conflicts.
	spaceSkill := env.newSkill(member, space, "csvtool")
	member.must(204, "PUT", route(a, mGamma), nil, nil)
	setB := "spaces/" + space + "/agents/" + b + "/skills"
	admin.must(200, "PUT", setB, map[string]any{"skill_ids": []string{bid, spaceSkill.ID}}, nil)
	admin.expectError(409, "AGENT_SKILL_NAME_TAKEN", "PUT", route(b, mGamma), nil)
	admin.must(200, "PUT", setB, map[string]any{"skill_ids": []string{spaceSkill.ID}}, nil)
	if _, err := os.Stat(filepath.Join(env.root, bkey)); !os.IsNotExist(err) {
		t.Fatalf("removed file still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.root, akey)); err != nil {
		t.Fatalf("other Agent lost its file: %v", err)
	}
	// Install the new catalog release on B. Reinstalling on A must keep its exact old bytes and version.
	oldBytes, err := os.ReadFile(filepath.Join(env.root, akey))
	if err != nil {
		t.Fatal(err)
	}
	const updated = "---\nname: pdf-tools\ndescription: Pinned file\n---\nnew market file"
	env.putSkill(mAlpha, "3.0.0", map[string]string{"SKILL.md": updated}, nil, 9)
	env.mustImport(market.ImportOptions{})
	admin.must(204, "PUT", route(b, mAlpha), nil, nil)
	member.must(204, "PUT", route(a, mAlpha), nil, nil)
	var av, bv string
	env.scalar(&av, "SELECT market_version FROM openfox.agent_market_skill_installs WHERE id=$1", aid)
	env.scalar(&bv, "SELECT market_version FROM openfox.agent_market_skill_installs WHERE agent_id=$1 AND market_skill_id=$2", b, mAlpha)
	env.scalar(&bkey, "SELECT files->0->>'Key' FROM openfox.agent_market_skill_installs WHERE agent_id=$1 AND market_skill_id=$2", b, mAlpha)
	first, e1 := os.ReadFile(filepath.Join(env.root, akey))
	second, e2 := os.ReadFile(filepath.Join(env.root, bkey))
	if e1 != nil || e2 != nil || string(first) != string(oldBytes) || string(second) != updated || av == bv || bv != "3.0.0" {
		t.Fatalf("version or file isolation failed: %s %s %v %v", av, bv, e1, e2)
	}
	if n := env.count("SELECT count(*) FROM openfox.skills WHERE space_id=$1 AND market_skill_id IS NOT NULL", space); n != 0 {
		t.Fatalf("market snapshots in space library: %d", n)
	}
	env.exec("UPDATE openfox.space_members SET role='VIEWER' WHERE space_id=$1 AND user_id=$2", space, s3MemberID)
	if got := targets(member, "Market QA"); len(got) != 0 {
		t.Fatalf("viewer targets: %v", got)
	}
	member.expectError(403, "", "PUT", route(a, mGamma), nil)
}
