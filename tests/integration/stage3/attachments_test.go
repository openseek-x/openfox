package stage3

// 任务与对话附件的集成测试：真实 HTTP 处理器 + 真实 PostgreSQL（FORCE RLS）+ 本地文件存储。
// 覆盖上传 / 列表 / 下载 / 删除、类型白名单与内容嗅探、大小与数量上限、权限（读不到任务的人、跨组织）、
// 对话附件的先传后绑、绑定校验，以及附件随 Run 上下文下发给执行电脑。

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	testPNG  = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{1}, 64)...)
	testHTML = []byte("<html><script>alert(1)</script></html>")
)

type attachmentView struct {
	ID             string `json:"id"`
	OwnerKind      string `json:"owner_kind"`
	TaskID         string `json:"task_id"`
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
	Name           string `json:"name"`
	ContentType    string `json:"content_type"`
	SizeBytes      int64  `json:"size_bytes"`
	UploadedBy     string `json:"uploaded_by"`
	Previewable    bool   `json:"previewable"`
	CanDelete      bool   `json:"can_delete"`
}

func uploadFile(t *testing.T, client *s3Client, path, filename string, data []byte) s3Response {
	t.Helper()
	body, contentType := docMultipart(t, nil, filename, data)
	return client.raw(http.MethodPost, path, body, contentType)
}

// mustUpload 上传并要求 201，返回附件。
func mustUpload(t *testing.T, client *s3Client, path, filename string, data []byte) attachmentView {
	t.Helper()
	response := uploadFile(t, client, path, filename, data)
	if response.Status != http.StatusCreated {
		t.Fatalf("upload %s = %d %s", filename, response.Status, response.Body)
	}
	var view attachmentView
	response.decode(t, &view)
	return view
}

func taskAttachmentsPath(spaceID, taskID string) string {
	return "spaces/" + spaceID + "/tasks/" + taskID + "/attachments"
}

// storedObjects 返回存储根目录下 attachments/ 里的对象数量。
func storedObjects(t *testing.T, root string) int {
	t.Helper()
	count := 0
	_ = filepath.WalkDir(filepath.Join(root, "attachments"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && !strings.HasPrefix(entry.Name(), ".upload-") {
			count++
		}
		return nil
	})
	return count
}

func TestTaskAttachments(t *testing.T) {
	env := newS3Env(t)
	root := t.TempDir()
	withAvatarStorage(env, root)
	space := env.space.ID
	manager, member := env.manager(), env.member()
	task := env.createTask(manager, space, nil)
	path := taskAttachmentsPath(space, task.ID)

	var uploaded attachmentView
	response := uploadFile(t, member, path, "../../shot.png", testPNG)
	if response.Status != http.StatusCreated {
		t.Fatalf("upload = %d %s", response.Status, response.Body)
	}
	response.decode(t, &uploaded)
	if uploaded.OwnerKind != "TASK" || uploaded.TaskID != task.ID || uploaded.Name != "shot.png" || uploaded.ContentType != "image/png" ||
		uploaded.SizeBytes != int64(len(testPNG)) || uploaded.UploadedBy != s3MemberID || !uploaded.Previewable || !uploaded.CanDelete || uploaded.MessageID != "" {
		t.Fatalf("uploaded = %+v", uploaded)
	}
	if storedObjects(t, root) != 1 {
		t.Fatalf("stored objects = %d, want 1", storedObjects(t, root))
	}

	t.Run("list and download", func(t *testing.T) {
		var list struct {
			Attachments []attachmentView `json:"attachments"`
		}
		manager.must(http.StatusOK, http.MethodGet, path, nil, &list)
		if len(list.Attachments) != 1 || list.Attachments[0].ID != uploaded.ID || !list.Attachments[0].CanDelete {
			t.Fatalf("list = %+v", list)
		}
		// 默认下载：attachment，不被浏览器当页面解释。
		download := member.must(http.StatusOK, http.MethodGet, "attachments/"+uploaded.ID, nil, nil)
		if !bytes.Equal(download.Body, testPNG) || !strings.HasPrefix(download.Header.Get("Content-Disposition"), "attachment;") ||
			download.Header.Get("X-Content-Type-Options") != "nosniff" || download.Header.Get("Content-Security-Policy") != "sandbox" {
			t.Fatalf("download headers = %v", download.Header)
		}
		// 图片预览：显式 inline，仍带 nosniff。
		preview := manager.must(http.StatusOK, http.MethodGet, "attachments/"+uploaded.ID+"?disposition=inline", nil, nil)
		if !strings.HasPrefix(preview.Header.Get("Content-Disposition"), "inline;") || preview.Header.Get("Content-Type") != "image/png" ||
			preview.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("preview headers = %v", preview.Header)
		}
	})

	t.Run("type allow list and content sniffing", func(t *testing.T) {
		for _, name := range []string{"run.exe", "script.sh", "noext", "archive.png.exe"} {
			uploadFile(t, member, path, name, []byte("MZ\x90\x00")).expectStatus(t, http.StatusUnsupportedMediaType)
		}
		// 扩展名与内容不一致（HTML 冒充 PNG）。
		response := uploadFile(t, member, path, "fake.png", testHTML)
		response.expectStatus(t, http.StatusUnsupportedMediaType)
		if response.code() != "ATTACHMENT_TYPE_NOT_ALLOWED" {
			t.Fatalf("code = %s", response.code())
		}
		// HTML / SVG 允许上传，但以纯文本保存：只能下载，?disposition=inline 无效。
		for name, data := range map[string][]byte{"page.html": testHTML, "pic.svg": []byte("<svg onload=alert(1)/>")} {
			var view attachmentView
			view = mustUpload(t, member, path, name, data)
			if view.ContentType != "text/plain; charset=utf-8" || view.Previewable {
				t.Fatalf("%s view = %+v", name, view)
			}
			download := member.must(http.StatusOK, http.MethodGet, "attachments/"+view.ID+"?disposition=inline", nil, nil)
			if !strings.HasPrefix(download.Header.Get("Content-Disposition"), "attachment;") || strings.Contains(download.Header.Get("Content-Type"), "html") ||
				strings.Contains(download.Header.Get("Content-Type"), "svg") {
				t.Fatalf("%s download headers = %v", name, download.Header)
			}
			member.must(http.StatusNoContent, http.MethodDelete, "attachments/"+view.ID, nil, nil)
		}
		// 被拒绝的上传不留下存储对象。
		if storedObjects(t, root) != 1 {
			t.Fatalf("stored objects = %d, want 1", storedObjects(t, root))
		}
	})

	t.Run("size limit", func(t *testing.T) {
		response := uploadFile(t, member, path, "big.txt", bytes.Repeat([]byte("a"), 20<<20+1))
		response.expectStatus(t, http.StatusRequestEntityTooLarge)
		if response.code() != "ATTACHMENT_TOO_LARGE" {
			t.Fatalf("code = %s", response.code())
		}
		if storedObjects(t, root) != 1 {
			t.Fatalf("stored objects = %d, want 1", storedObjects(t, root))
		}
	})

	t.Run("permissions and tenant isolation", func(t *testing.T) {
		// 不是私有空间成员的组织成员、组织访客：读不到任务，上传 / 列表 / 下载 / 删除都是 404。
		for name, client := range map[string]*s3Client{"other": env.other(), "guest": env.guest()} {
			client.expectError(http.StatusNotFound, "", http.MethodGet, path, nil)
			client.expectError(http.StatusNotFound, "", http.MethodGet, "attachments/"+uploaded.ID, nil)
			client.expectError(http.StatusNotFound, "", http.MethodDelete, "attachments/"+uploaded.ID, nil)
			if response := uploadFile(t, client, path, "x.txt", []byte("x")); response.Status != http.StatusNotFound {
				t.Fatalf("%s upload = %d %s", name, response.Status, response.Body)
			}
		}
		// 跨组织：另一个组织的成员既不能经本组织路径、也不能经自己组织的路径读到。
		outsider := env.outsider()
		if response := outsider.doIn(env.org, http.MethodGet, "attachments/"+uploaded.ID, nil); response.Status == http.StatusOK {
			t.Fatalf("outsider via org path = %d", response.Status)
		}
		outsider.doIn(env.otherOrg, http.MethodGet, "attachments/"+uploaded.ID, nil).expectStatus(t, http.StatusNotFound)
		outsider.doIn(env.otherOrg, http.MethodDelete, "attachments/"+uploaded.ID, nil).expectStatus(t, http.StatusNotFound)
		// 不存在的附件 ID 与格式错误的 ID 同样是 404。
		member.expectError(http.StatusNotFound, "", http.MethodGet, "attachments/"+s3NewID(t), nil)
		member.expectError(http.StatusNotFound, "", http.MethodGet, "attachments/not-a-uuid", nil)
	})

	t.Run("delete by uploader or space admin only", func(t *testing.T) {
		var byManager attachmentView
		byManager = mustUpload(t, manager, path, "mine.txt", []byte("manager file"))
		// 普通空间成员不能删别人的附件（能读，所以是 403 而不是 404）。
		member.expectError(http.StatusForbidden, "", http.MethodDelete, "attachments/"+byManager.ID, nil)
		// 空间管理员（空间 OWNER）可以删别人的附件，存储对象一并清理。
		before := storedObjects(t, root)
		manager.must(http.StatusNoContent, http.MethodDelete, "attachments/"+uploaded.ID, nil, nil)
		if storedObjects(t, root) != before-1 {
			t.Fatalf("stored objects = %d, want %d", storedObjects(t, root), before-1)
		}
		member.expectError(http.StatusNotFound, "", http.MethodGet, "attachments/"+uploaded.ID, nil)
		// 上传人删自己的。
		var mine attachmentView
		mine = mustUpload(t, member, path, "own.txt", []byte("own"))
		member.must(http.StatusNoContent, http.MethodDelete, "attachments/"+mine.ID, nil, nil)
		member.expectError(http.StatusNotFound, "", http.MethodDelete, "attachments/"+mine.ID, nil)
		manager.must(http.StatusNoContent, http.MethodDelete, "attachments/"+byManager.ID, nil, nil)
		if storedObjects(t, root) != 0 || env.count(`SELECT count(*) FROM openfox.attachments WHERE task_id = $1::uuid`, task.ID) != 0 {
			t.Fatalf("leftovers: objects=%d", storedObjects(t, root))
		}
	})

	t.Run("per task limit holds under concurrency", func(t *testing.T) {
		limited := env.createTask(manager, space, nil)
		env.exec(`INSERT INTO openfox.attachments (organization_id, owner_kind, space_id, task_id, name, file_key, content_type, size_bytes, sha256, uploaded_by)
			SELECT $1::uuid, 'TASK', $2::uuid, $3::uuid, 'seed-' || n, 'attachments/seed/' || n, 'text/plain; charset=utf-8', 1, repeat('a', 64), $4::uuid
			  FROM generate_series(1, 98) AS n`, env.org, space, limited.ID, s3MemberID)
		statuses := parallel(5, func(int) int {
			return uploadFile(t, member, taskAttachmentsPath(space, limited.ID), "c.txt", []byte("c")).Status
		})
		if countStatus(statuses, http.StatusCreated) != 2 || countStatus(statuses, http.StatusConflict) != 3 {
			t.Fatalf("statuses = %v, want 2 created and 3 conflicts", statuses)
		}
		if n := env.count(`SELECT count(*) FROM openfox.attachments WHERE task_id = $1::uuid`, limited.ID); n != 100 {
			t.Fatalf("attachments = %d, want 100", n)
		}
		response := uploadFile(t, member, taskAttachmentsPath(space, limited.ID), "d.txt", []byte("d"))
		response.expectStatus(t, http.StatusConflict)
		if response.code() != "ATTACHMENT_LIMIT" {
			t.Fatalf("code = %s", response.code())
		}
	})

	t.Run("deleted task hides attachments", func(t *testing.T) {
		gone := env.createTask(manager, space, nil)
		var view attachmentView
		view = mustUpload(t, manager, taskAttachmentsPath(space, gone.ID), "a.txt", []byte("a"))
		env.exec(`UPDATE openfox.tasks SET deleted_at = clock_timestamp(), deleted_by = $2::uuid WHERE id = $1::uuid`, gone.ID, s3ManagerID)
		manager.expectError(http.StatusNotFound, "", http.MethodGet, "attachments/"+view.ID, nil)
		manager.expectError(http.StatusNotFound, "", http.MethodGet, taskAttachmentsPath(space, gone.ID), nil)
	})
}

func TestConversationAttachmentsAndRunDelivery(t *testing.T) {
	env := newS3Env(t)
	root := t.TempDir()
	withAvatarStorage(env, root)
	ensureDirectory(env)
	space := env.space.ID
	owner, member := env.owner(), env.member()
	executor := env.registerSkillDevice(owner, env.org, "ORGANIZATION")
	bystander := env.registerSkillDevice(owner, env.org, "ORGANIZATION")

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
	uploadPath := "conversations/" + conversation.ID + "/attachments"

	var note attachmentView
	note = mustUpload(t, owner, uploadPath, "notes.txt", []byte("remember the milk"))
	if note.OwnerKind != "CONVERSATION" || note.ConversationID != conversation.ID || note.MessageID != "" || note.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("note = %+v", note)
	}
	var picture attachmentView
	picture = mustUpload(t, owner, uploadPath, "pic.png", testPNG)

	t.Run("pending attachments are private to the uploader", func(t *testing.T) {
		member.expectError(http.StatusNotFound, "", http.MethodGet, "attachments/"+note.ID, nil)
		member.expectError(http.StatusNotFound, "", http.MethodDelete, "attachments/"+note.ID, nil)
		// 不是会话成员的人不能往会话里传附件。
		if response := uploadFile(t, member, uploadPath, "x.txt", []byte("x")); response.Status != http.StatusNotFound {
			t.Fatalf("member upload = %d %s", response.Status, response.Body)
		}
		uploadFile(t, owner, uploadPath, "bad.png", testHTML).expectStatus(t, http.StatusUnsupportedMediaType)
		download := owner.must(http.StatusOK, http.MethodGet, "attachments/"+note.ID, nil, nil)
		if string(download.Body) != "remember the milk" {
			t.Fatalf("download = %q", download.Body)
		}
	})

	messagesPath := "conversations/" + conversation.ID + "/messages"
	mentions := []map[string]string{{"agent_id": agent.ID}}
	messageCount := func() int {
		return env.count(`SELECT count(*) FROM openfox.messages WHERE conversation_id = $1::uuid`, conversation.ID)
	}

	t.Run("binding is validated", func(t *testing.T) {
		before := messageCount()
		for name, ids := range map[string][]string{
			"unknown":   {s3NewID(t)},
			"malformed": {"not-a-uuid"},
			"duplicate": {note.ID, note.ID},
		} {
			response := owner.do(http.MethodPost, messagesPath, map[string]any{"body": "hi", "client_message_id": "bad-" + name, "mentions": mentions, "attachment_ids": ids})
			if response.Status != http.StatusBadRequest {
				t.Fatalf("%s = %d %s", name, response.Status, response.Body)
			}
		}
		// 绑定失败时整条消息回滚，不留下半条消息。
		if messageCount() != before {
			t.Fatalf("messages = %d, want %d", messageCount(), before)
		}
	})

	var posted struct {
		ID          string           `json:"id"`
		Body        string           `json:"body"`
		Attachments []attachmentView `json:"attachments"`
	}
	owner.must(http.StatusCreated, http.MethodPost, messagesPath, map[string]any{
		"body": "", "client_message_id": "with-files", "mentions": mentions, "attachment_ids": []string{note.ID, picture.ID},
	}, &posted)
	t.Run("message carries attachments", func(t *testing.T) {
		if posted.Body != "请查看附件。" || len(posted.Attachments) != 2 || posted.Attachments[0].ID != note.ID || posted.Attachments[0].MessageID != posted.ID {
			t.Fatalf("posted = %+v", posted)
		}
		var list struct {
			Messages []struct {
				ID          string           `json:"id"`
				Attachments []attachmentView `json:"attachments"`
			} `json:"messages"`
		}
		owner.must(http.StatusOK, http.MethodGet, messagesPath, nil, &list)
		found := false
		for _, message := range list.Messages {
			if message.ID == posted.ID {
				found = len(message.Attachments) == 2
			} else if message.Attachments == nil {
				t.Fatalf("message %s must carry an empty attachments array", message.ID)
			}
		}
		if !found {
			t.Fatalf("list = %+v", list)
		}
		// 已发送的附件不能再绑定到别的消息，也不能删除。
		owner.expectError(http.StatusBadRequest, "", http.MethodPost, messagesPath,
			map[string]any{"body": "again", "client_message_id": "reuse", "mentions": mentions, "attachment_ids": []string{note.ID}})
		owner.expectError(http.StatusForbidden, "", http.MethodDelete, "attachments/"+note.ID, nil)
		// 会话对非成员不可见，已发送的附件同样读不到。
		member.expectError(http.StatusNotFound, "", http.MethodGet, "attachments/"+note.ID, nil)
	})

	t.Run("attachments are delivered with the run context", func(t *testing.T) {
		var otherNote attachmentView
		otherNote = mustUpload(t, owner, uploadPath, "later.txt", []byte("not in this run"))
		owner.must(http.StatusCreated, http.MethodPost, messagesPath, map[string]any{
			"body": "second", "client_message_id": "second", "attachment_ids": []string{otherNote.ID},
		}, nil)

		insertRun := func(conversationID string) string {
			id := s3NewID(t)
			env.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id,
					config_version, grant_version, actor_id, state, lease_expires_at)
				SELECT c.organization_id, $2::uuid, c.space_id, c.id, c.agent_id, a.device_id, g.id,
				       a.current_config_version, g.permission_version, $4::uuid, 'PREPARING', clock_timestamp() + interval '5 minutes'
				  FROM openfox.conversations c
				  JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
				  JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
				 WHERE c.organization_id = $1::uuid AND c.id = $3::uuid`, env.org, id, conversationID, s3OwnerID)
			return id
		}
		runID := insertRun(conversation.ID)
		env.exec(`UPDATE openfox.conversation_inputs SET status = 'ASSIGNED', run_id = $2::uuid, assigned_at = clock_timestamp()
			WHERE organization_id = $1::uuid AND message_id = $3::uuid`, env.org, runID, posted.ID)

		base := "/v1/device/organizations/" + env.org + "/runs/" + runID
		var context struct {
			Attachments []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				ContentType string `json:"content_type"`
				SizeBytes   int64  `json:"size_bytes"`
			} `json:"attachments"`
		}
		contextResponse := env.deviceCall(executor, http.MethodGet, base+"/context?generation=1", nil)
		contextResponse.expectStatus(t, http.StatusOK)
		contextResponse.decode(t, &context)
		if len(context.Attachments) != 2 || context.Attachments[0].ID != note.ID || context.Attachments[0].Name != "notes.txt" ||
			context.Attachments[0].SizeBytes != int64(len("remember the milk")) || context.Attachments[1].ID != picture.ID {
			t.Fatalf("context attachments = %+v", context.Attachments)
		}
		download := env.deviceCall(executor, http.MethodGet, base+"/attachments/"+note.ID+"?generation=1", nil)
		download.expectStatus(t, http.StatusOK)
		if string(download.Body) != "remember the milk" || !strings.HasPrefix(download.Header.Get("Content-Disposition"), "attachment;") ||
			download.Header.Get("Cache-Control") != "no-store" || download.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("device download = %q %v", download.Body, download.Header)
		}
		// 不属于这个 Run 输入的附件、别的电脑、错误或缺少的 generation、未知令牌都拿不到。
		env.deviceCall(executor, http.MethodGet, base+"/attachments/"+otherNote.ID+"?generation=1", nil).expectStatus(t, http.StatusNotFound)
		env.deviceCall(executor, http.MethodGet, base+"/attachments/"+s3NewID(t)+"?generation=1", nil).expectStatus(t, http.StatusNotFound)
		env.deviceCall(bystander, http.MethodGet, base+"/attachments/"+note.ID+"?generation=1", nil).expectStatus(t, http.StatusNotFound)
		env.deviceCall(executor, http.MethodGet, base+"/attachments/"+note.ID+"?generation=2", nil).expectStatus(t, http.StatusConflict)
		env.deviceCall(executor, http.MethodGet, base+"/attachments/"+note.ID, nil).expectStatus(t, http.StatusBadRequest)
		env.deviceCall(skillDevice{token: "bad"}, http.MethodGet, base+"/attachments/"+note.ID+"?generation=1", nil).expectStatus(t, http.StatusUnauthorized)
		// 没有附件的 Run：attachments 是空数组而不是 null。
		var plain struct {
			ID string `json:"id"`
		}
		owner.must(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": space, "agent_id": agent.ID}, &plain)
		empty := insertRun(plain.ID)
		response := env.deviceCall(executor, http.MethodGet, "/v1/device/organizations/"+env.org+"/runs/"+empty+"/context?generation=1", nil)
		response.expectStatus(t, http.StatusOK)
		if !strings.Contains(string(response.Body), `"attachments":[]`) {
			t.Fatalf("empty attachments must be [], body = %s", response.Body)
		}
	})

	t.Run("pending upload limit", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			if response := uploadFile(t, owner, uploadPath, "p.txt", []byte("p")); response.Status != http.StatusCreated {
				// 前面的测试已有 1 个待发送（later.txt 已发送，其余为 0），所以 20 个都应成功。
				t.Fatalf("pending upload %d = %d %s", i, response.Status, response.Body)
			}
		}
		response := uploadFile(t, owner, uploadPath, "p.txt", []byte("p"))
		response.expectStatus(t, http.StatusConflict)
		if response.code() != "ATTACHMENT_PENDING_LIMIT" {
			t.Fatalf("code = %s", response.code())
		}
	})
}
