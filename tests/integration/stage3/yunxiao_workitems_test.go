package stage3

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/yunxiao"
	"openfox/internal/secrets"
)

type yunxiaoFake struct {
	mu                                 sync.Mutex
	title, description, status, holder string
	count, reads, writes, code         int
	archived                           int
	entered                            chan struct{}
	release                            chan struct{}
	blockIdentity                      bool
}

func (f *yunxiaoFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reads++
	title, description, status, holder, count, code, entered, release := f.title, f.description, f.status, f.holder, f.count, f.code, f.entered, f.release
	archived := f.archived
	blockIdentity := f.blockIdentity
	f.mu.Unlock()
	if blockIdentity && r.URL.Path == "/oapi/v1/platform/user" {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
	}
	token := r.Header.Get("x-yunxiao-token")
	if !strings.HasPrefix(token, "synthetic-") {
		w.WriteHeader(401)
		return
	}
	if code != 0 && strings.HasSuffix(r.URL.Path, "workitems:search") {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(code)
		return
	}
	if r.Method != "GET" && !(r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "projects:search") || strings.HasSuffix(r.URL.Path, "workitems:search"))) {
		f.mu.Lock()
		f.writes++
		f.mu.Unlock()
		w.WriteHeader(405)
		return
	}
	user, email := holder, s3Email(s3OwnerID)
	if token == "synthetic-member" {
		user = "member-cloud"
		email = s3Email(s3MemberID)
	}
	if token == "synthetic-duplicate" {
		email = s3Email(s3MemberID)
	}
	if token == "synthetic-mismatch" {
		email = "other@example.test"
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/oapi/v1/platform/user":
		json.NewEncoder(w).Encode(map[string]string{"id": user, "name": "Cloud identity", "email": email})
	case strings.HasSuffix(r.URL.Path, "members:readByUser"):
		json.NewEncoder(w).Encode(map[string]string{"userId": user, "organizationId": "cloud-org", "status": "ENABLED"})
	case strings.HasSuffix(r.URL.Path, "projects:search"):
		yunxiaoPage(w, 1)
		fmt.Fprint(w, `[{"id":"project-a","name":"Cloud project","logicalStatus":"normal"}]`)
	case strings.HasSuffix(r.URL.Path, "/fields"):
		fmt.Fprint(w, `[{"id":"rank","name":"Importance","type":"CustomField","format":"list","options":[{"id":"999","displayValue":"Must","value":"must"}]}]`)
	case strings.HasSuffix(r.URL.Path, "/workitemTypes"):
		fmt.Fprint(w, `[{"id":"kind-a","name":"Custom type","categoryId":"Custom"}]`)
	case strings.HasSuffix(r.URL.Path, "workitems:search"):
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		items := []any{}
		for i := 0; i < count; i++ {
			logical := "normal"
			if i < archived {
				logical = "archived"
			}
			items = append(items, map[string]any{"id": fmt.Sprintf("item-%d", i), "subject": fmt.Sprintf("%s %d", title, i), "description": description, "formatType": "MARKDOWN", "categoryId": "Custom", "logicalStatus": logical, "space": map[string]string{"id": "project-a"}, "status": map[string]string{"id": status, "name": "Todo"}, "workitemType": map[string]string{"id": "kind-a", "name": "Custom type"}, "assignedTo": map[string]string{"id": "member-cloud", "name": "Member cloud"}, "customFieldValues": []any{map[string]any{"fieldId": "rank", "values": []any{map[string]string{"identifier": "999", "displayValue": "Must"}}}}})
		}
		yunxiaoPage(w, len(items))
		json.NewEncoder(w).Encode(items)
	default:
		w.WriteHeader(404)
	}
}
func yunxiaoPage(w http.ResponseWriter, n int) {
	w.Header().Set("x-total", fmt.Sprint(n))
	w.Header().Set("x-page", "1")
	w.Header().Set("x-per-page", "200")
	w.Header().Set("x-total-pages", "1")
}

type yunxiaoEnv struct {
	*s3Env
	fake       *yunxiaoFake
	client     *yunxiao.Client
	box        *secrets.Box
	connection integrations.Connection
}

func newYunxiaoEnv(t *testing.T) *yunxiaoEnv {
	e := newS3Env(t)
	f := &yunxiaoFake{title: "Remote", description: "Body", status: "state-a", holder: "owner-cloud", count: 1}
	sink := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(sink.Close)
	key := make([]byte, secrets.KeySize)
	rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	client := &yunxiao.Client{APIURL: sink.URL, HTTP: sink.Client()}
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, YunxiaoClient: client}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid)`, e.org, e.otherOrg)
	out := &yunxiaoEnv{s3Env: e, fake: f, client: client, box: box}
	t.Cleanup(func() {
		e.exec(`UPDATE openfox.import_sync_links SET state='PAUSED',lease=NULL WHERE organization_id=$1::uuid`, e.org)
	})
	return out
}
func (e *yunxiaoEnv) bind() {
	e.owner().must(200, "POST", "integrations/yunxiao/instance-token", map[string]string{"method": "token", "secret": "synthetic-owner", "company_id": "cloud-org"}, &e.connection, "Idempotency-Key", "yunxiao-org-1")
}
func TestYunxiaoIdentityHTTPPermissionsIdempotencyAndCAS(t *testing.T) {
	e := newYunxiaoEnv(t)
	body := map[string]string{"method": "token", "secret": "synthetic-owner", "company_id": "cloud-org"}
	e.member().expectError(403, "", "POST", "integrations/yunxiao/instance-token", body)
	e.guest().expectError(403, "", "POST", "integrations/yunxiao/instance-token", body)
	e.bind()
	var replay integrations.Connection
	e.owner().must(200, "POST", "integrations/yunxiao/instance-token", body, &replay, "Idempotency-Key", "yunxiao-org-1")
	if replay.ID != e.connection.ID {
		t.Fatal("duplicate organization")
	}
	body["secret"] = "synthetic-renewed"
	e.owner().expectError(409, "IDEMPOTENCY_CONFLICT", "POST", "integrations/yunxiao/instance-token", body, "Idempotency-Key", "yunxiao-org-1")
	var personal integrations.Connection
	input := map[string]string{"connection_id": e.connection.ID, "token": "synthetic-member"}
	e.guest().expectError(403, "", "POST", "integrations/yunxiao/personal-token", input)
	e.member().expectError(400, "", "POST", "integrations/yunxiao/personal-token", map[string]string{"connection_id": e.connection.ID, "token": "synthetic-mismatch"})
	e.member().must(200, "POST", "integrations/yunxiao/personal-token", input, &personal, "Idempotency-Key", "yunxiao-member-1")
	var empty bool
	if err := e.superuser.QueryRow(e.ctx, `SELECT credentials_sealed IS NULL AND credentials_key_id='' AND credentials_hint='' FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, personal.ID).Scan(&empty); err != nil || !empty {
		t.Fatalf("personal secret stored %v", err)
	}
	e.member().must(200, "POST", "integrations/yunxiao/personal-token", input, &replay, "Idempotency-Key", "yunxiao-member-1")
	if replay.ID != personal.ID {
		t.Fatal("personal replay duplicated")
	}
	path := "integrations/connections/" + e.connection.ID + "/yunxiao/credentials"
	e.owner().must(200, "PUT", path, map[string]any{"version": e.connection.Version, "token": "synthetic-renewed"}, &replay)
	e.owner().expectError(409, "", "PUT", path, map[string]any{"version": e.connection.Version, "token": "synthetic-owner"})
	e.fake.mu.Lock()
	e.fake.holder = "different-holder"
	e.fake.mu.Unlock()
	e.owner().expectError(400, "", "PUT", path, map[string]any{"version": replay.Version, "token": "synthetic-owner"})
	e.owner().must(200, "POST", "integrations/connections/"+e.connection.ID+"/disconnect", map[string]any{"version": replay.Version, "confirm_phrase": integrations.ConfirmPhrase("云效")}, nil)
	if err := e.superuser.QueryRow(e.ctx, `SELECT credentials_sealed IS NULL AND credentials_key_id='' FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, e.connection.ID).Scan(&empty); err != nil || !empty {
		t.Fatalf("disconnect kept PAT %v", err)
	}
	e.member().expectError(404, "", "POST", "integrations/yunxiao/personal-token", input, "Idempotency-Key", "yunxiao-member-1")
}
func (e *yunxiaoEnv) draft(sync bool) imports.Job {
	e.bind()
	var job imports.Job
	e.owner().must(201, "POST", "imports/yunxiao/project", map[string]string{"connection_id": e.connection.ID, "project_id": "project-a", "space_id": e.space.ID}, &job, "Idempotency-Key", "yunxiao-draft-1")
	var todo string
	e.superuser.QueryRow(e.ctx, `SELECT id::text FROM openfox.task_statuses WHERE organization_id=$1::uuid AND name='Todo'`, e.org).Scan(&todo)
	cfg := job.Config
	cfg.Options.ContinuousSync = sync
	cfg.Statuses = []imports.StatusMapping{{Source: yunxiao.StatusSource("kind-a", "state-a"), StatusID: todo}}
	cfg.Types = []imports.TypeMapping{{Source: yunxiao.TypeSource("kind-a"), UseDefault: true}}
	cfg.PriorityFields = []imports.PriorityFieldMapping{{TypeSource: yunxiao.TypeSource("kind-a"), FieldID: "rank"}}
	cfg.Priorities = []imports.PriorityMapping{{Source: yunxiao.PrioritySource("kind-a", "rank", "999"), Priority: "HIGH"}}
	e.owner().must(200, "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg}, &job)
	if job.Analysis == nil || len(job.Analysis.Issues) != 0 {
		e.t.Fatalf("incomplete config %+v", job.Analysis)
	}
	return job
}
func (e *yunxiaoEnv) run(job imports.Job) imports.Job {
	e.owner().must(200, "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion}, nil)
	worker := imports.Worker{Pool: e.database, Secrets: e.box, Yunxiao: e.client, Instance: "yunxiao-worker"}
	for i := 0; i < 10; i++ {
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			e.t.Fatal(err)
		}
		e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
		if job.Status == "SUCCEEDED" {
			return job
		}
		if job.Status == "FAILED" {
			e.t.Fatalf("import failed %+v", job)
		}
	}
	e.t.Fatalf("did not finish %+v", job)
	return job
}
func TestYunxiaoImportWorkerInboundAndSourceVersion(t *testing.T) {
	e := newYunxiaoEnv(t)
	job := e.run(e.draft(true))
	if job.Sync == nil || job.Sync.Direction != "INBOUND" {
		t.Fatalf("not inbound %+v", job.Sync)
	}
	e.owner().expectError(400, "", "PUT", "imports/"+job.ID+"/sync", map[string]any{"version": job.Sync.Version, "direction": "BIDIRECTIONAL"})
	var id, title, priority string
	var version int
	if err := e.superuser.QueryRow(e.ctx, `SELECT t.id::text,t.title,t.priority,t.version FROM openfox.tasks t JOIN openfox.import_mappings m ON m.organization_id=t.organization_id AND m.task_id=t.id WHERE m.organization_id=$1::uuid AND m.source_system='yunxiao' AND m.external_id='item-0'`, e.org).Scan(&id, &title, &priority, &version); err != nil {
		t.Fatal(err)
	}
	if title != "Remote 0" || priority != "HIGH" {
		t.Fatalf("wrong imported task %s/%s", title, priority)
	}
	e.owner().must(200, "PATCH", "spaces/"+e.space.ID+"/tasks/"+id, map[string]any{"version": version, "title": "Local edit"}, nil)
	worker := imports.Worker{Pool: e.database, Secrets: e.box, Yunxiao: e.client, Instance: "yunxiao-sync"}
	tick := func() {
		e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE organization_id=$1::uuid AND job_id=$2::uuid`, e.org, job.ID)
		if err := worker.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	e.superuser.QueryRow(e.ctx, `SELECT title,version FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, id).Scan(&title, &version)
	if title != "Local edit" {
		t.Fatal("unchanged source overwrote local")
	}
	e.fake.mu.Lock()
	e.fake.title = "Source changed"
	e.fake.count = 2
	e.fake.mu.Unlock()
	tick()
	e.superuser.QueryRow(e.ctx, `SELECT title FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, id).Scan(&title)
	if title != "Source changed 0" {
		t.Fatalf("incoming source missing %s", title)
	}
	var n int
	e.superuser.QueryRow(e.ctx, `SELECT count(*) FROM openfox.import_mappings WHERE organization_id=$1::uuid AND source_system='yunxiao'`, e.org).Scan(&n)
	if n != 2 {
		t.Fatalf("new items count %d", n)
	}
	tick()
	e.superuser.QueryRow(e.ctx, `SELECT count(*) FROM openfox.import_mappings WHERE organization_id=$1::uuid AND source_system='yunxiao'`, e.org).Scan(&n)
	if n != 2 {
		t.Fatal("replay duplicated")
	}
	e.fake.mu.Lock()
	writes := e.fake.writes
	e.fake.mu.Unlock()
	if writes != 0 {
		t.Fatal("outbound requests observed")
	}
}

func TestYunxiaoSyncRejectsStaleReadAndRetainsCopiesOnDisconnect(t *testing.T) {
	e := newYunxiaoEnv(t)
	job := e.run(e.draft(true))
	var task, title string
	if err := e.superuser.QueryRow(e.ctx, `SELECT task_id::text FROM openfox.import_mappings WHERE organization_id=$1::uuid AND source_system='yunxiao' AND external_id='item-0'`, e.org).Scan(&task); err != nil {
		t.Fatal(err)
	}
	worker := imports.Worker{Pool: e.database, Secrets: e.box, Yunxiao: e.client, Instance: "yunxiao-race"}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	e.fake.mu.Lock()
	e.fake.title = "Stale source"
	e.fake.entered = entered
	e.fake.release = release
	e.fake.mu.Unlock()
	e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE organization_id=$1::uuid AND job_id=$2::uuid`, e.org, job.ID)
	done := make(chan error, 1)
	go func() { done <- worker.RunOrganization(e.ctx, e.org) }()
	select {
	case <-entered:
	case <-e.ctx.Done():
		t.Fatal("read did not enter")
	}
	var fresh integrations.Connection
	e.owner().must(200, "PUT", "integrations/connections/"+e.connection.ID+"/yunxiao/credentials", map[string]any{"version": e.connection.Version, "token": "synthetic-renewed"}, &fresh)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	e.superuser.QueryRow(e.ctx, `SELECT title FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, task).Scan(&title)
	if title != "Remote 0" {
		t.Fatalf("stale token read applied %s", title)
	}
	e.fake.mu.Lock()
	e.fake.entered = nil
	e.fake.release = nil
	e.fake.code = 429
	e.fake.mu.Unlock()
	e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE organization_id=$1::uuid AND job_id=$2::uuid`, e.org, job.ID)
	if err := worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	var delayed bool
	var code string
	e.superuser.QueryRow(e.ctx, `SELECT next_check>clock_timestamp()+interval '9 minutes',error_code FROM openfox.import_sync_links WHERE organization_id=$1::uuid AND job_id=$2::uuid`, e.org, job.ID).Scan(&delayed, &code)
	if !delayed || code != "RATE_LIMITED" {
		t.Fatalf("429 retry not retained %v/%s", delayed, code)
	}
	e.owner().must(200, "POST", "integrations/connections/"+fresh.ID+"/disconnect", map[string]any{"version": fresh.Version, "confirm_phrase": integrations.ConfirmPhrase("云效")}, nil)
	var state string
	e.superuser.QueryRow(e.ctx, `SELECT state FROM openfox.import_sync_links WHERE organization_id=$1::uuid AND job_id=$2::uuid`, e.org, job.ID).Scan(&state)
	if state != "DISCONNECTED" {
		t.Fatal("sync continued after disconnect")
	}
	e.superuser.QueryRow(e.ctx, `SELECT title FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid AND deleted_at IS NULL`, e.org, task).Scan(&title)
	if title != "Remote 0" {
		t.Fatal("imported copy removed")
	}
}

func TestYunxiaoDraftRequiresExplicitMappingAndCurrentConnection(t *testing.T) {
	e := newYunxiaoEnv(t)
	e.bind()
	var job imports.Job
	request := map[string]string{"connection_id": e.connection.ID, "project_id": "project-a", "space_id": e.space.ID}
	e.member().expectError(403, "", "POST", "imports/yunxiao/project", request)
	e.owner().must(201, "POST", "imports/yunxiao/project", request, &job, "Idempotency-Key", "yunxiao-map-draft")
	var same imports.Job
	e.owner().must(201, "POST", "imports/yunxiao/project", request, &same, "Idempotency-Key", "yunxiao-map-draft")
	if same.ID != job.ID {
		t.Fatal("draft replay duplicated")
	}
	e.owner().expectError(400, "IMPORT_CONFIG_INCOMPLETE", "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion})
	if job.Analysis == nil || len(job.Analysis.Types) != 1 || job.Analysis.Types[0].SourceLabel != "Custom type" || len(job.Analysis.PriorityFields) != 1 {
		t.Fatalf("missing source evidence %+v", job.Analysis)
	}
	var todo string
	e.superuser.QueryRow(e.ctx, `SELECT id::text FROM openfox.task_statuses WHERE organization_id=$1::uuid AND name='Todo'`, e.org).Scan(&todo)
	cfg := job.Config
	cfg.Statuses = []imports.StatusMapping{{Source: yunxiao.StatusSource("kind-a", "state-a"), StatusID: todo}}
	cfg.Types = []imports.TypeMapping{{Source: yunxiao.TypeSource("kind-a"), UseDefault: true}}
	cfg.PriorityFields = []imports.PriorityFieldMapping{{TypeSource: yunxiao.TypeSource("kind-a"), FieldID: ""}}
	e.owner().must(200, "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg}, &job)
	var fresh integrations.Connection
	e.owner().must(200, "PUT", "integrations/connections/"+e.connection.ID+"/yunxiao/credentials", map[string]any{"version": e.connection.Version, "token": "synthetic-renewed"}, &fresh)
	e.owner().expectError(409, "", "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion})
}

func TestYunxiaoConcurrentCreateAndWorkersDoNotDuplicate(t *testing.T) {
	e := newYunxiaoEnv(t)
	owner := e.owner()
	body := map[string]string{"method": "token", "secret": "synthetic-owner", "company_id": "cloud-org"}
	replies := make(chan s3Response, 2)
	for i := 0; i < 2; i++ {
		go func() {
			replies <- owner.do("POST", "integrations/yunxiao/instance-token", body, "Idempotency-Key", "yunxiao-org-1")
		}()
	}
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		r := <-replies
		if r.Status != 200 {
			t.Fatalf("concurrent bind %d %s", r.Status, r.Body)
		}
		var c integrations.Connection
		r.decode(t, &c)
		ids[c.ID] = true
	}
	if len(ids) != 1 {
		t.Fatal("concurrent request duplicated connection")
	}
	job := e.draft(true)
	owner.must(200, "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion}, nil)
	a := imports.Worker{Pool: e.database, Secrets: e.box, Yunxiao: e.client, Instance: "yunxiao-a"}
	b := imports.Worker{Pool: e.database, Secrets: e.box, Yunxiao: e.client, Instance: "yunxiao-b"}
	for round := 0; round < 3; round++ {
		done := make(chan error, 2)
		go func() { done <- a.RunOrganization(e.ctx, e.org) }()
		go func() { done <- b.RunOrganization(e.ctx, e.org) }()
		for i := 0; i < 2; i++ {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	}
	owner.must(200, "GET", "imports/"+job.ID, nil, &job)
	if job.Status != "SUCCEEDED" {
		t.Fatalf("concurrent worker status %s", job.Status)
	}
	if n := e.count(`SELECT count(*) FROM openfox.import_mappings WHERE organization_id=$1::uuid AND source_system='yunxiao'`, e.org); n != 1 {
		t.Fatalf("duplicate task mappings %d", n)
	}
}

func TestYunxiaoFinalWriteSerializesRoleRevocation(t *testing.T) {
	for _, kind := range []string{"organization", "credentials", "personal", "draft"} {
		t.Run(kind, func(t *testing.T) {
			e := newYunxiaoEnv(t)
			user := s3AdminID
			client := e.admin()
			method := "POST"
			path := "integrations/yunxiao/instance-token"
			var body any = map[string]string{"method": "token", "secret": "synthetic-owner", "company_id": "cloud-org"}
			if kind != "organization" {
				e.bind()
			}
			if kind == "personal" {
				user = s3MemberID
				client = e.member()
				path = "integrations/yunxiao/personal-token"
				body = map[string]string{"connection_id": e.connection.ID, "token": "synthetic-member"}
			}
			if kind == "credentials" {
				method = "PUT"
				path = "integrations/connections/" + e.connection.ID + "/yunxiao/credentials"
				body = map[string]any{"version": e.connection.Version, "token": "synthetic-renewed"}
			}
			if kind == "draft" {
				e.addSpaceMember(e.space.ID, user, "MEMBER")
				path = "imports/yunxiao/project"
				body = map[string]string{"connection_id": e.connection.ID, "project_id": "project-a", "space_id": e.space.ID}
			}
			entered, release := make(chan struct{}, 1), make(chan struct{})
			e.fake.mu.Lock()
			e.fake.entered = entered
			e.fake.release = release
			e.fake.blockIdentity = kind != "draft"
			e.fake.mu.Unlock()
			response := make(chan s3Response, 1)
			go func() { response <- client.do(method, path, body) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("external verification not reached")
			}
			lock, err := e.superuser.Begin(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback(e.ctx)
			if kind == "organization" {
				_, err = lock.Exec(e.ctx, `SELECT user_id FROM openfox.organization_members WHERE organization_id=$1 AND user_id=$2 FOR UPDATE`, e.org, user)
			} else {
				_, err = lock.Exec(e.ctx, `SELECT id FROM openfox.integration_connections WHERE organization_id=$1 AND id=$2 FOR UPDATE`, e.org, e.connection.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			close(release)
			deadline := time.Now().Add(5 * time.Second)
			waiting := false
			for time.Now().Before(deadline) {
				e.exec(`SELECT pg_stat_clear_snapshot()`)
				e.scalar(&waiting, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock')`)
				if waiting {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("final save did not wait on lock")
			}
			if _, err = lock.Exec(e.ctx, `UPDATE openfox.organization_members SET role='GUEST',authorization_version=authorization_version+1 WHERE organization_id=$1 AND user_id=$2`, e.org, user); err != nil {
				t.Fatal(err)
			}
			if err = lock.Commit(e.ctx); err != nil {
				t.Fatal(err)
			}
			got := <-response
			if got.Status != 403 {
				t.Fatalf("revoked final save %d %s", got.Status, got.Body)
			}
			if n := e.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1 AND created_by=$2`, e.org, user); n != 0 {
				t.Fatalf("revoked writer created %d bindings", n)
			}
			if kind == "draft" {
				if n := e.count(`SELECT count(*) FROM openfox.import_jobs WHERE organization_id=$1`, e.org); n != 0 {
					t.Fatal("revoked writer persisted source draft")
				}
			}
			if kind == "credentials" {
				var version uint64
				e.scalar(&version, `SELECT version FROM openfox.integration_connections WHERE id=$1`, e.connection.ID)
				if version != e.connection.Version {
					t.Fatal("revoked writer changed PAT")
				}
			}
		})
	}
}
func TestYunxiaoPersonalReplayRequiresCurrentProof(t *testing.T) {
	e := newYunxiaoEnv(t)
	e.bind()
	input := map[string]string{"connection_id": e.connection.ID, "token": "synthetic-member"}
	key := "proof-replay"
	e.member().must(200, "POST", "integrations/yunxiao/personal-token", input, nil, "Idempotency-Key", key)
	e.exec(`UPDATE openfox.users SET email='changed-yunxiao@example.test' WHERE id=$1`, s3MemberID)
	t.Cleanup(func() { e.exec(`UPDATE openfox.users SET email=$2 WHERE id=$1`, s3MemberID, s3Email(s3MemberID)) })
	e.member().expectError(409, "", "POST", "integrations/yunxiao/personal-token", input, "Idempotency-Key", key)
	e.exec(`UPDATE openfox.users SET email=$2 WHERE id=$1`, s3MemberID, s3Email(s3MemberID))
	e.member().must(200, "POST", "integrations/yunxiao/personal-token", input, nil, "Idempotency-Key", key)
	e.owner().must(200, "PUT", "integrations/connections/"+e.connection.ID+"/yunxiao/credentials", map[string]any{"version": e.connection.Version, "token": "synthetic-renewed"}, nil)
	e.member().expectError(409, "", "POST", "integrations/yunxiao/personal-token", input, "Idempotency-Key", key)
}

func TestYunxiaoVerifiedIdentityRecheckedByWorker(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprintf("disconnect_%t", revoke), func(t *testing.T) {
			e := newYunxiaoEnv(t)
			e.bind()
			e.addSpaceMember(e.space.ID, s3MemberID, "MEMBER")
			var personal integrations.Connection
			e.member().must(200, "POST", "integrations/yunxiao/personal-token", map[string]string{"connection_id": e.connection.ID, "token": "synthetic-member"}, &personal)
			job := e.draft(false)
			if len(job.Analysis.Users) != 1 || job.Analysis.Users[0].VerifiedBinding == nil || job.Analysis.Users[0].VerifiedBinding.ID != personal.ID {
				t.Fatalf("no verified source suggestion %+v", job.Analysis.Users)
			}
			cfg := job.Config
			cfg.Users = []imports.UserMapping{{SourceID: job.Analysis.Users[0].SourceID, Mode: "member", UserID: s3MemberID, VerifiedBindingID: personal.ID}}
			e.owner().must(200, "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg}, &job)
			e.owner().must(200, "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion}, nil)
			if revoke {
				e.member().must(200, "POST", "integrations/connections/"+personal.ID+"/disconnect", map[string]any{"version": personal.Version, "confirm_phrase": integrations.ConfirmPhrase("云效")}, nil)
			}
			w := imports.Worker{Pool: e.database, Secrets: e.box, Yunxiao: e.client, Instance: "identity-recheck"}
			for i := 0; i < 5; i++ {
				if err := w.RunOrganization(e.ctx, e.org); err != nil {
					t.Fatal(err)
				}
			}
			e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
			if job.Status != "SUCCEEDED" {
				t.Fatalf("job %s %s", job.Status, job.ErrorMessage)
			}
			n := e.count(`SELECT count(*) FROM openfox.task_assignees WHERE organization_id=$1 AND user_id=$2`, e.org, s3MemberID)
			want := 1
			if revoke {
				want = 0
			}
			if n != want {
				t.Fatalf("assignment %d want %d", n, want)
			}
			if n := e.count(`SELECT count(*) FROM openfox.external_accounts WHERE organization_id=$1 AND provider='yunxiao' AND linked_user_id IS NOT NULL`, e.org); n != 0 {
				t.Fatal("automatic proof modified global account link")
			}
		})
	}
}

func TestYunxiaoLowestPriorityKeepsOrganizationConfirmation(t *testing.T) {
	e := newYunxiaoEnv(t)
	job := e.draft(false)
	cfg := job.Config
	cfg.Priorities[0].Priority = "LOWEST"
	e.owner().must(200, "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg}, &job)
	impact := job.Analysis.PriorityImpact
	if !impact.UpgradeRequired || impact.LowestCount != 1 {
		t.Fatalf("missing O01 impact %+v", impact)
	}
	e.owner().expectError(409, "PRIORITY_UPGRADE_CONFIRMATION_REQUIRED", "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion})
	e.owner().must(200, "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion, "confirm_priority_upgrade": true, "organization_version": impact.OrganizationVersion}, nil)
	w := imports.Worker{Pool: e.database, Secrets: e.box, Yunxiao: e.client, Instance: "lowest"}
	for i := 0; i < 5; i++ {
		if err := w.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
	}
	var priority string
	e.scalar(&priority, `SELECT priority FROM openfox.tasks WHERE organization_id=$1 AND title='Remote 0'`, e.org)
	if priority != "LOWEST" {
		t.Fatalf("priority downgraded %s", priority)
	}
}
func TestYunxiaoRollbackRetainsCopiesAndStopsWork(t *testing.T) {
	e := newYunxiaoEnv(t)
	completed := e.run(e.draft(true))
	completed.Config.Options.ContinuousSync = false
	var pending imports.Job
	e.owner().must(201, "POST", "imports/yunxiao/project", map[string]string{"connection_id": e.connection.ID, "project_id": "project-a", "space_id": e.space.ID}, &pending)
	e.owner().must(200, "PUT", "imports/"+pending.ID+"/config", map[string]any{"config_version": pending.ConfigVersion, "config": completed.Config}, &pending)
	e.owner().must(200, "POST", "imports/"+pending.ID+"/start", map[string]any{"config_version": pending.ConfigVersion}, nil)
	e.owner().must(201, "POST", "imports/yunxiao/project", map[string]string{"connection_id": e.connection.ID, "project_id": "project-a", "space_id": e.space.ID}, nil)
	sql, err := os.ReadFile("testdata/yunxiao-rollback.sql")
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`SELECT set_config('openfox.rollback_organization_id',$1,false)`, e.org)
	for i := 0; i < 2; i++ {
		e.exec(string(sql))
	}
	if n := e.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1 AND title='Remote 0'`, e.org); n != 1 {
		t.Fatalf("rollback removed copied tasks %d", n)
	}
	if n := e.count(`SELECT count(*) FROM openfox.import_mappings WHERE organization_id=$1 AND source_system='yunxiao'`, e.org); n != 1 {
		t.Fatalf("rollback lost source identity %d", n)
	}
	if n := e.count(`SELECT count(*) FROM openfox.import_jobs WHERE organization_id=$1 AND source='yunxiao_api' AND status IN('DRAFT','QUEUED','RUNNING','CANCEL_REQUESTED')`, e.org); n != 0 {
		t.Fatalf("rollback left active jobs %d", n)
	}
	w := imports.Worker{Pool: e.database, Secrets: e.box, Yunxiao: e.client, Instance: "post-rollback"}
	if err = w.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1`, e.org); n != 1 {
		t.Fatal("old queued job resumed")
	}
}

func TestYunxiaoIdentityUniquenessAndTenantIsolation(t *testing.T) {
	e := newYunxiaoEnv(t)
	e.bind()
	e.owner().must(200, "POST", "integrations/yunxiao/personal-token", map[string]string{"connection_id": e.connection.ID, "token": "synthetic-owner"}, nil)
	e.member().expectError(409, "", "POST", "integrations/yunxiao/personal-token", map[string]string{"connection_id": e.connection.ID, "token": "synthetic-duplicate"})
	other := e.outsider().doIn(e.otherOrg, "POST", "integrations/yunxiao/instance-token", map[string]string{"method": "token", "secret": "synthetic-owner", "company_id": "cloud-org"})
	if other.Status != 200 {
		t.Fatalf("separate tenant same source %d %s", other.Status, other.Body)
	}
	var foreign integrations.Connection
	other.decode(t, &foreign)
	e.owner().expectError(404, "", "POST", "integrations/yunxiao/personal-token", map[string]string{"connection_id": foreign.ID, "token": "synthetic-owner"})
	e.owner().expectError(404, "", "POST", "imports/yunxiao/project", map[string]string{"connection_id": foreign.ID, "project_id": "project-a", "space_id": e.space.ID})
	e.owner().expectError(404, "", "PUT", "integrations/connections/"+foreign.ID+"/yunxiao/credentials", map[string]any{"version": foreign.Version, "token": "synthetic-owner"})
}

func TestYunxiaoEmptyAndArchivedSnapshotsHaveExactCounts(t *testing.T) {
	for _, input := range []struct {
		name            string
		total, archived int
	}{{"empty", 0, 0}, {"all_archived", 2, 2}, {"mixed", 2, 1}} {
		t.Run(input.name, func(t *testing.T) {
			e := newYunxiaoEnv(t)
			e.bind()
			e.fake.mu.Lock()
			e.fake.count = input.total
			e.fake.archived = input.archived
			e.fake.mu.Unlock()
			var job imports.Job
			e.owner().must(201, "POST", "imports/yunxiao/project", map[string]string{"connection_id": e.connection.ID, "project_id": "project-a", "space_id": e.space.ID}, &job)
			e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
			if job.Analysis == nil || job.Analysis.ArchivedExcluded != input.archived || job.TotalCount != input.total-input.archived {
				t.Fatalf("wrong snapshot counts %+v", job)
			}
			if n := e.count(`SELECT count(*) FROM openfox.import_items WHERE organization_id=$1 AND job_id=$2`, e.org, job.ID); n != input.total-input.archived {
				t.Fatalf("placeholder/source rows %d", n)
			}
		})
	}
}
