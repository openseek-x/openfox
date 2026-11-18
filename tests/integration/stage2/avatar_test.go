package stage2

import (
	"bytes"
	"net/http"
	"testing"
)

func TestAgentsUploadedAvatar(t *testing.T) {
	env := newAgEnv(t)
	owner := env.client(agOwnerID)
	spaceID := env.createSpace("shared")
	iconID, foreignID := agNewID(t), agNewID(t)
	for _, icon := range []struct{ organizationID, id, userID string }{
		{env.org, iconID, agOwnerID}, {env.otherOrg, foreignID, agOutsiderID},
	} {
		env.exec(`INSERT INTO openfox.uploaded_icons (organization_id,id,storage_key,content_type,size_bytes,sha256,created_by)
			VALUES ($1::uuid,$2::uuid,$3,'image/png',1,repeat('a',64),$4::uuid)`, icon.organizationID, icon.id, "icons/test/"+icon.id, icon.userID)
	}
	url := "/api/v1/organizations/" + env.org + "/icons/" + iconID
	avatar := map[string]any{"kind": "upload", "value": url, "color": "#22C55E"}
	agent := env.createAgent(owner, spaceID, "SHARED", map[string]any{"avatar": avatar})
	if !bytes.Contains(agent.Avatar, []byte(url)) {
		t.Fatalf("created avatar = %s", agent.Avatar)
	}
	for _, invalidURL := range []string{
		"https://example.test/avatar.png", "/api/v1/organizations/" + env.otherOrg + "/icons/" + foreignID,
		"/api/v1/organizations/" + env.org + "/icons/" + foreignID,
		"/api/v1/organizations/" + env.org + "/icons/" + agNewID(t), url + "?x=1", url + "/../other",
	} {
		invalid := map[string]any{"kind": "upload", "value": invalidURL}
		owner.expectError(http.StatusBadRequest, "", http.MethodPatch, agentPath(spaceID, agent.ID)+"/profile",
			map[string]any{"profile_version": agent.ProfileVersion, "avatar": invalid})
		owner.expectError(http.StatusBadRequest, "", http.MethodPost, "spaces/"+spaceID+"/agents",
			map[string]any{"name": "Invalid " + agSuffix(t), "kind": "CODE", "visibility": "SHARED", "config": env.configBody(nil), "avatar": invalid})
	}
	unchanged := env.getAgent(owner, spaceID, agent.ID)
	if unchanged.ProfileVersion != agent.ProfileVersion || !bytes.Equal(unchanged.Avatar, agent.Avatar) {
		t.Fatalf("invalid writes changed avatar: %+v", unchanged)
	}
	env.client(agMemberID).expectError(http.StatusForbidden, "", http.MethodPatch, agentPath(spaceID, agent.ID)+"/profile",
		map[string]any{"profile_version": agent.ProfileVersion, "avatar": avatar})
	var patched agAgent
	owner.must(http.StatusOK, http.MethodPatch, agentPath(spaceID, agent.ID)+"/profile", map[string]any{"profile_version": agent.ProfileVersion, "avatar": nil}, &patched)
	owner.must(http.StatusOK, http.MethodPatch, agentPath(spaceID, agent.ID)+"/profile", map[string]any{"profile_version": patched.ProfileVersion, "avatar": avatar}, &patched)
	if !bytes.Contains(env.getAgent(owner, spaceID, agent.ID).Avatar, []byte(url)) {
		t.Fatal("uploaded avatar did not persist")
	}
}
