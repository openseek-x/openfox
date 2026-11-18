package stage11f

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
	"openfox/internal/mcpconnect"
	"openfox/internal/membership"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

const memberID = "00000000-0000-0000-0000-000000000004"

func TestVariantSecretAndRepositoryInheritance(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	setup, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = setup.Exec(ctx, `INSERT INTO openfox.users(id,email) VALUES($1::uuid,'variant-member@example.test')`, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err = setup.Exec(ctx, `INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'MEMBER')`, org, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err = setup.Exec(ctx, `INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES($1::uuid,$2::uuid,$3::uuid,'OWNER')`, org, space, memberID); err != nil {
		t.Fatal(err)
	}
	setup.Close(ctx)
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 6, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	box, err := secrets.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	sel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: ownerID, AuthorizationVersion: 1}
	actor := agents.Actor{UserID: ownerID, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	devicesList, err := (devices.PostgresStore{Pool: pool}).List(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	var grant string
	for _, d := range devicesList {
		if d.ID == deviceID {
			grant = d.GrantID
		}
	}
	if grant == "" {
		t.Fatal("device grant missing")
	}
	store := agents.PostgresStore{Pool: pool, Secrets: box}
	parent, err := store.Create(ctx, sel, actor, space, agents.CreateInput{Name: "11f-secret-parent", Kind: agents.KindCode, Visibility: agents.VisibilityShared, Config: agents.ConfigInput{ComputerID: deviceID, GrantID: grant, Runtime: "codex", Model: "gpt-5", Instructions: "parent-only-instruction"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PutEnvVar(ctx, sel, actor, space, parent.ID, "PARENT_KEY", "sample-secret", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateRepo(ctx, sel, actor, space, parent.ID, agents.RepoInput{URL: "https://example.test/sample.git"}, ""); err != nil {
		t.Fatal(err)
	}
	// 同一父所有者的可信电脑必须能读取继承的环境变量，避免安全门禁一律拒绝。
	setup, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `UPDATE openfox.physical_devices SET owner_user_id=$1::uuid WHERE id=$2::uuid`, ownerID, deviceID)
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	trustedChild, _, err := store.CreateVariant(ctx, sel, actor, space, parent.ID, agents.VariantInput{SpaceID: space}, "")
	if err != nil {
		t.Fatal(err)
	}
	assertTrustedVariantContext(t, ctx, pool, sel, url, trustedChild, grant, box)
	// 组织 Git 连接只供父级使用；非父所有者的分身不得物化其 connection_id。
	connectionID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	setup, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,created_by,credentials_sealed)
		VALUES($1::uuid,$2::uuid,'github','ORGANIZATION','stage11f-github','Stage 11f GitHub',$3::uuid,$4)`, org, connectionID, ownerID, []byte("test-sealed-credential"))
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateRepo(ctx, sel, actor, space, parent.ID,
		agents.RepoInput{URL: "https://github.com/example/stage11f.git", ConnectionID: connectionID}, ""); err != nil {
		t.Fatal(err)
	}
	// 父级 MCP 凭据仍绑定父 ID，分身只读其公开列表而不建立复制记录。
	mcpID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	mcp := mcpconnect.Config{Name: "parent-tools", Scope: mcpconnect.Public, Transport: "http", URL: "https://mcp.example.test", AuthMode: "api_key", AuthHeader: "X-API-Key", Credential: "test-credential"}
	sealed, err := mcpconnect.Seal(box, org, parent.ID, mcpID, mcp)
	if err != nil {
		t.Fatal(err)
	}
	setup, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `INSERT INTO openfox.agent_mcp_servers(organization_id,agent_id,id,name,scope,config,status,secret_ciphertext,secret_nonce,secret_key_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4,'PUBLIC',$5,'connected',$6,$7,$8)`, org, parent.ID, mcpID, mcp.Name, mcp.Redacted(), sealed.Ciphertext, sealed.Nonce, sealed.KeyID)
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	memberSel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: memberID, AuthorizationVersion: 1}
	member := agents.Actor{UserID: memberID, Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	child, _, err := store.CreateVariant(ctx, memberSel, member, space, parent.ID, agents.VariantInput{SpaceID: space}, "")
	if err != nil {
		t.Fatal(err)
	}
	// 构造待派发的非父所有者 Run；不能从 child 或父级解密得到父环境变量。
	conversationID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	runID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	setup, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid)`, org, conversationID, space, child.ID, memberID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state)
		SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8,g.permission_version,$9::uuid,'PREPARING'
		FROM openfox.device_grants g WHERE g.organization_id=$1::uuid AND g.id=$7::uuid`, org, runID, space, conversationID, child.ID, deviceID, grant, child.CurrentConfigVersion, memberID)
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 赋予有效租约后由真实设备 Context 路径检查，不依赖只解密环境变量的独立函数。
	setup, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `UPDATE openfox.runs SET lease_expires_at=clock_timestamp()+interval '5 minutes'
		WHERE organization_id=$1::uuid AND id=$2::uuid`, org, runID)
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = pool.RunForOrganization(ctx, org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return memberSel, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			_, e := (dispatch.Store{}).ContextForDevice(ctx, tx, org, deviceID, runID, 1, box, true)
			if !errors.Is(e, dispatch.ErrAuthorizationRevoked) {
				return errors.New("non-owner device received inherited MCP or connected repository")
			}
			_, e = dispatch.LoadRunEnvironment(ctx, tx, box, org, runID, child.ID, deviceID)
			if !errors.Is(e, dispatch.ErrAuthorizationRevoked) {
				return errors.New("non-owner device received parent environment")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := store.ListMCP(ctx, memberSel, member, space, child.ID)
	if err != nil || tools.CanEdit || len(tools.Servers) != 1 || tools.Servers[0].ID != mcpID || tools.Servers[0].Config.Credential != mcpconnect.Mask {
		t.Fatalf("inherited MCP=%+v err=%v", tools, err)
	}
	env, err := store.ListEnvVars(ctx, memberSel, member, space, child.ID)
	if err != nil || env.CanViewValues || len(env.EnvVars) != 0 {
		t.Fatalf("inherited secret leaked: %+v %v", env, err)
	}
	if _, err = store.PutEnvVar(ctx, memberSel, member, space, child.ID, "OWN_KEY", "test", nil); !errors.Is(err, agents.ErrForbidden) {
		t.Fatalf("secret override permitted: %v", err)
	}
	repos, err := store.ListRepos(ctx, memberSel, member, space, child.ID)
	if err != nil || len(repos) != 1 || repos[0].ConnectionID != "" {
		t.Fatalf("inherited repos=%+v %v", repos, err)
	}
	vars := map[string]string{}
	overridden, _, err := store.CreateVariant(ctx, memberSel, member, space, parent.ID, agents.VariantInput{SpaceID: space, Overrides: agents.VariantOverrides{Env: &vars}}, "")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := store.ListEnvVars(ctx, memberSel, member, space, overridden.ID)
	if err != nil || !empty.CanViewValues || len(empty.EnvVars) != 0 {
		t.Fatalf("empty override=%+v %v", empty, err)
	}
	if _, err = store.PutEnvVar(ctx, memberSel, member, space, overridden.ID, "OWN_KEY", "test", nil); err != nil {
		t.Fatal(err)
	}
	own, err := store.ListEnvVars(ctx, memberSel, member, space, overridden.ID)
	if err != nil || len(own.EnvVars) != 1 || own.EnvVars[0].Value == nil || *own.EnvVars[0].Value != "test" {
		t.Fatalf("own secret=%+v %v", own, err)
	}
	parentEnv, err := store.ListEnvVars(ctx, sel, actor, space, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := parentEnv.EnvVars[0].Version
	if _, err = store.PutEnvVar(ctx, sel, actor, space, parent.ID, "PARENT_KEY", "rotated-sample", &version); err != nil {
		t.Fatal(err)
	}
	env, err = store.ListEnvVars(ctx, memberSel, member, space, child.ID)
	if err != nil || env.CanViewValues || len(env.EnvVars) != 0 {
		t.Fatalf("rotated secret leaked: %+v %v", env, err)
	}
	err = pool.RunForOrganization(ctx, org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return memberSel, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			var count int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.agent_env_vars WHERE organization_id=$1::uuid AND agent_id=$2::uuid`, org, child.ID).Scan(&count); e != nil {
				return e
			}
			if count != 0 {
				return errors.New("parent secret was materialized to another owner's variant")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	// 分开验证组织 Git 连接及 MCP 密文：环境组显式覆盖后，不应靠环境变量拒绝掩盖其他旁路。
	setup, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `UPDATE openfox.agent_mcp_servers SET secret_ciphertext=NULL,secret_nonce=NULL,secret_key_id=NULL
		WHERE organization_id=$1::uuid AND agent_id=$2::uuid AND id=$3::uuid`, org, parent.ID, mcpID)
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertVariantContextRevoked(t, ctx, pool, memberSel, url, overridden, grant, box)
	setup, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `UPDATE openfox.agent_mcp_servers SET secret_ciphertext=$1,secret_nonce=$2,secret_key_id=$3
		WHERE organization_id=$4::uuid AND agent_id=$5::uuid AND id=$6::uuid`, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, org, parent.ID, mcpID)
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	noRepos := []agents.RepoInput{}
	mcpOnly, _, err := store.CreateVariant(ctx, memberSel, member, space, parent.ID,
		agents.VariantInput{SpaceID: space, Overrides: agents.VariantOverrides{Env: &vars, Repos: &noRepos}}, "")
	if err != nil {
		t.Fatal(err)
	}
	assertVariantContextRevoked(t, ctx, pool, memberSel, url, mcpOnly, grant, box)
	// 回归：父级删除最后一个环境变量后，子侧残留旧密文也必须阻止派发；不自动删除历史数据。
	staleChild, _, err := store.CreateVariant(ctx, memberSel, member, space, parent.ID,
		agents.VariantInput{SpaceID: space, Overrides: agents.VariantOverrides{Repos: &noRepos}}, "")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := box.Seal([]byte("stale-test-value"), secrets.EnvAAD(org, staleChild.ID, "PARENT_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	setup, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `INSERT INTO openfox.agent_env_vars(organization_id,agent_id,name,value_ciphertext,value_nonce,key_id,updated_by)
		VALUES($1::uuid,$2::uuid,'PARENT_KEY',$3,$4,$5,$6::uuid)`, org, staleChild.ID, stale.Ciphertext, stale.Nonce, stale.KeyID, ownerID)
	if err == nil {
		_, err = setup.Exec(ctx, `UPDATE openfox.agent_mcp_servers SET secret_ciphertext=NULL,secret_nonce=NULL,secret_key_id=NULL
			WHERE organization_id=$1::uuid AND agent_id=$2::uuid AND id=$3::uuid`, org, parent.ID, mcpID)
	}
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	currentParentEnv, err := store.ListEnvVars(ctx, sel, actor, space, parent.ID)
	if err != nil || len(currentParentEnv.EnvVars) != 1 {
		t.Fatalf("parent env before delete=%+v err=%v", currentParentEnv, err)
	}
	if err := store.DeleteEnvVar(ctx, sel, actor, space, parent.ID, "PARENT_KEY"); err != nil {
		t.Fatal(err)
	}
	assertStaleVariantEnvironmentRevoked(t, ctx, pool, memberSel, url, staleChild, grant, box)
	// 恢复只用于测试的 MCP 密文，避免改变后续撤权断言。
	setup, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `UPDATE openfox.agent_mcp_servers SET secret_ciphertext=$1,secret_nonce=$2,secret_key_id=$3
		WHERE organization_id=$4::uuid AND agent_id=$5::uuid AND id=$6::uuid`, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, org, parent.ID, mcpID)
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 显式覆盖的指令属于分身本人；失去父级访问权后仍可编辑。
	ownInstruction := "own-instruction"
	editable, _, err := store.CreateVariant(ctx, memberSel, member, space, parent.ID,
		agents.VariantInput{SpaceID: space, Overrides: agents.VariantOverrides{Instructions: &ownInstruction}}, "")
	if err != nil {
		t.Fatal(err)
	}
	// 父级改为私有后，分身本人仍可读基本资料，但不能读继承指令、仓库与 MCP。
	private := agents.VisibilityPrivate
	if _, err := store.UpdateProfile(ctx, sel, actor, space, parent.ID, agents.ProfileInput{Visibility: &private, ProfileVersion: parent.ProfileVersion}); err != nil {
		t.Fatal(err)
	}
	detail, err := store.Get(ctx, memberSel, member, space, child.ID)
	if err != nil || detail.Name != child.Name || !detail.ParentUnavailable || detail.Parent != nil || detail.Config == nil || detail.Config.Instructions != "" {
		t.Fatalf("revoked child detail=%+v err=%v", detail, err)
	}
	items, err := store.List(ctx, memberSel, member, space, agents.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == child.ID && (!item.ParentUnavailable || item.Config == nil || item.Config.Instructions != "") {
			t.Fatalf("revoked child listed with inherited content: %+v", item)
		}
	}
	ownDetail, err := store.Get(ctx, memberSel, member, space, editable.ID)
	if err != nil || !ownDetail.ParentUnavailable || ownDetail.Config == nil || ownDetail.Config.Instructions != ownInstruction {
		t.Fatalf("own override after revocation=%+v err=%v", ownDetail, err)
	}
	ownConfig := agents.ConfigInput{
		Instructions: "own-updated", ComputerID: ownDetail.Config.ComputerID, GrantID: ownDetail.Config.GrantID,
		Runtime: ownDetail.Config.Runtime, Model: ownDetail.Config.Model,
	}
	ownDetail, err = store.CreateConfig(ctx, memberSel, member, space, editable.ID, ownDetail.Version, ownConfig)
	if err != nil || ownDetail.Config == nil || ownDetail.Config.Instructions != "own-updated" || !ownDetail.ParentUnavailable {
		t.Fatalf("own override write after revocation=%+v err=%v", ownDetail, err)
	}
	tools, err = store.ListMCP(ctx, memberSel, member, space, child.ID)
	if err != nil || len(tools.Servers) != 0 {
		t.Fatalf("revoked MCP=%+v err=%v", tools, err)
	}
	repos, err = store.ListRepos(ctx, memberSel, member, space, child.ID)
	if err != nil || len(repos) != 0 {
		t.Fatalf("revoked repos=%+v err=%v", repos, err)
	}
	if _, err = store.ListConfigs(ctx, memberSel, member, space, child.ID); !errors.Is(err, agents.ErrForbidden) {
		t.Fatalf("revoked history exposed: %v", err)
	}
}

// assertVariantContextRevoked 用有效设备租约覆盖真实上下文入口，确保父级秘密不下发。
func assertVariantContextRevoked(t *testing.T, ctx context.Context, pool *postgres.Pool, sel tenancy.OrganizationSelection, url string, child agents.Agent, grant string, box *secrets.Box) {
	t.Helper()
	conversationID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	runID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	setup, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid)`, org, conversationID, space, child.ID, memberID)
	if err == nil {
		_, err = setup.Exec(ctx, `INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,lease_expires_at)
			SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8,g.permission_version,$9::uuid,'PREPARING',clock_timestamp()+interval '5 minutes'
			FROM openfox.device_grants g WHERE g.organization_id=$1::uuid AND g.id=$7::uuid`, org, runID, space, conversationID, child.ID, deviceID, grant, child.CurrentConfigVersion, memberID)
	}
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			_, e := (dispatch.Store{}).ContextForDevice(ctx, tx, org, deviceID, runID, 1, box, true)
			if !errors.Is(e, dispatch.ErrAuthorizationRevoked) {
				return errors.New("inherited connection or MCP credential reached device context")
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
}

// assertTrustedVariantContext 验证父所有者在本人实体电脑上可领取并解密合法继承值。
func assertTrustedVariantContext(t *testing.T, ctx context.Context, pool *postgres.Pool, sel tenancy.OrganizationSelection, url string, child agents.Agent, grant string, box *secrets.Box) {
	t.Helper()
	conversationID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	runID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	reservationID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	offerID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	setup, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid)`, org, conversationID, space, child.ID, ownerID)
	if err == nil {
		_, err = setup.Exec(ctx, `INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state)
			SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8,g.permission_version,$9::uuid,'QUEUED'
			FROM openfox.device_grants g WHERE g.organization_id=$1::uuid AND g.id=$7::uuid`, org, runID, space, conversationID, child.ID, deviceID, grant, child.CurrentConfigVersion, ownerID)
	}
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil })
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceOrganizationWorker, selector, func(ctx context.Context, tx pgx.Tx) error {
		var created string
		if e := tx.QueryRow(ctx, `SELECT openfox_private.dispatch_reserve_capacity($1::uuid,$2::uuid,$3::uuid,1,$4::uuid,$5::uuid,$6::uuid,clock_timestamp()+interval '5 minutes')::text`, org, reservationID, runID, deviceID, child.ID, offerID).Scan(&created); e != nil {
			return e
		}
		if created != offerID {
			return errors.New("offer ID mismatch")
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.runs SET reservation_id=$1::uuid,offer_id=$2::uuid WHERE organization_id=$3::uuid AND id=$4::uuid`, reservationID, offerID, org, runID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	deviceSel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: deviceID, AuthorizationVersion: 1}
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceDeviceRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return deviceSel, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			lease, e := (dispatch.Store{}).Claim(ctx, tx, org, deviceID, offerID)
			if e != nil {
				return e
			}
			if lease.RunID != runID {
				return errors.New("trusted claim returned another Run")
			}
			env, e := dispatch.LoadRunEnvironment(ctx, tx, box, org, runID, child.ID, deviceID)
			if e != nil {
				return e
			}
			if len(env) != 1 || env[0].Name != "PARENT_KEY" || env[0].Value != "sample-secret" {
				return errors.New("trusted child did not inherit the parent environment")
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	// 领取事务已提交后再走完整上下文，确保 MCP 查询及环境变量授权同时成立。
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceDeviceRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return deviceSel, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			result, e := (dispatch.Store{}).ContextForDevice(ctx, tx, org, deviceID, runID, 1, box, true)
			if e != nil {
				return e
			}
			if result.RunID != runID || result.AgentID != child.ID {
				return errors.New("trusted child received another Run context")
			}
			env, e := dispatch.LoadRunEnvironment(ctx, tx, box, org, runID, child.ID, deviceID)
			if e != nil {
				return e
			}
			if len(env) != 1 || env[0].Name != "PARENT_KEY" || env[0].Value != "sample-secret" {
				return errors.New("trusted child context lost the parent environment")
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
}

// assertStaleVariantEnvironmentRevoked 用父级已清空、子级仍存旧密文的夹具复核运行派发闭锁。
func assertStaleVariantEnvironmentRevoked(t *testing.T, ctx context.Context, pool *postgres.Pool, sel tenancy.OrganizationSelection, url string, child agents.Agent, grant string, box *secrets.Box) {
	t.Helper()
	conversationID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	runID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	setup, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid)`, org, conversationID, space, child.ID, memberID)
	if err == nil {
		_, err = setup.Exec(ctx, `INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state)
			SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8,g.permission_version,$9::uuid,'QUEUED'
			FROM openfox.device_grants g WHERE g.organization_id=$1::uuid AND g.id=$7::uuid`, org, runID, space, conversationID, child.ID, deviceID, grant, child.CurrentConfigVersion, memberID)
	}
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			_, e := dispatch.LoadRunEnvironment(ctx, tx, box, org, runID, child.ID, deviceID)
			if !errors.Is(e, dispatch.ErrAuthorizationRevoked) {
				return errors.New("stale inherited child ciphertext was not blocked")
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
}
