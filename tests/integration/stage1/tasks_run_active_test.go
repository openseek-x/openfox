package stage1

import (
	"net/http"
	"testing"
)

// 删除任务：任务上有进行中的 Run 时返回 409 RUN_ACTIVE；Run 结束后可以删除。
func TestTasksDeleteRejectsActiveRun(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	agent := env.insertAgent(space.ID, cfgOwnerID, "SHARED")
	task := env.newTask(env.owner, space, nil)

	conversationID, runID := cfgNewID(t), cfgNewID(t)
	env.exec(`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, task_id, owner_user_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid)`,
		env.org, conversationID, space.ID, agent, task.ID, cfgOwnerID)
	env.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, task_id, agent_id, computer_id,
			grant_id, config_version, grant_version, actor_id, state)
		SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, g.device_id, g.id, 1, 1, $7::uuid, 'RUNNING'
		  FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $8::uuid`,
		env.org, runID, space.ID, conversationID, task.ID, agent, cfgOwnerID, env.deviceID)

	current := env.getTask(env.owner, space, task.ID)
	env.owner.expectError(http.StatusConflict, "RUN_ACTIVE", http.MethodDelete,
		taskPath(space, task.ID)+"?version="+itoa(current.Version), nil)
	// 拒绝时不写墓碑。
	if after := env.getTask(env.owner, space, task.ID); after.Version != current.Version {
		t.Fatalf("version changed after rejected delete: %d -> %d", current.Version, after.Version)
	}

	env.exec(`UPDATE openfox.runs SET state = 'SUCCEEDED', finished_at = clock_timestamp(), version = version + 1
		WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, runID)
	env.owner.must(http.StatusNoContent, http.MethodDelete, taskPath(space, task.ID)+"?version="+itoa(current.Version), nil, nil)
}
