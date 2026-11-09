package stage3

import (
	"net/http"
	"testing"
)

func TestSpaceUploadedIcon(t *testing.T) {
	env := newS3Env(t)
	iconID, foreignID := s3NewID(t), s3NewID(t)
	for _, icon := range []struct{ organizationID, id, userID string }{
		{env.org, iconID, s3OwnerID}, {env.otherOrg, foreignID, s3OutsiderID},
	} {
		env.exec(`INSERT INTO openfox.uploaded_icons (organization_id,id,storage_key,content_type,size_bytes,sha256,created_by)
			VALUES ($1::uuid,$2::uuid,$3,'image/png',1,repeat('a',64),$4::uuid)`, icon.organizationID, icon.id, "icons/test/"+icon.id, icon.userID)
	}
	space := env.space
	path := spacePath(space.ID)
	for _, icon := range []any{"emoji:🚀", "upload:" + iconID, "orbit:#22C55E", nil} {
		env.owner().must(http.StatusOK, http.MethodPatch, path, map[string]any{"version": space.Version, "icon": icon}, &space)
		loaded := env.getSpace(env.owner(), space.ID)
		if icon == nil {
			if loaded.Icon != nil {
				t.Fatalf("clear = %v", loaded.Icon)
			}
		} else if loaded.Icon == nil || *loaded.Icon != icon.(string) {
			t.Fatalf("saved = %v, want %v", loaded.Icon, icon)
		}
	}
	for _, invalid := range []string{"upload:" + foreignID, "upload:" + s3NewID(t), "upload:../../other", "emoji:"} {
		env.owner().expectError(http.StatusBadRequest, "", http.MethodPatch, path, map[string]any{"version": space.Version, "icon": invalid})
		env.owner().expectError(http.StatusBadRequest, "", http.MethodPost, "spaces", map[string]any{"name": "Invalid " + s3Suffix(t), "visibility": "shared", "icon": invalid})
	}
	env.member().expectError(http.StatusForbidden, "", http.MethodPatch, path, map[string]any{"version": space.Version, "icon": "upload:" + iconID})
	if loaded := env.getSpace(env.owner(), space.ID); loaded.Version != space.Version || loaded.Icon != nil {
		t.Fatalf("invalid writes changed icon: %+v", loaded)
	}
	var created SpaceView
	env.owner().must(http.StatusCreated, http.MethodPost, "spaces", map[string]any{"name": "Image " + s3Suffix(t), "visibility": "shared", "icon": "upload:" + iconID}, &created)
	if created.Icon == nil || *created.Icon != "upload:"+iconID {
		t.Fatalf("create = %+v", created)
	}
}
