package stage3

import (
	"fmt"
	"net/http"
	"testing"
)

func TestReviewIterationLinksRespectSpaceAndState(t *testing.T) {
	env := newS3Env(t)
	other := env.createSpace("private")
	patchIterSettings(env, env.owner(), other.ID, map[string]any{"enabled": true, "mode": "manual"})
	iteration := createIteration(env, other.ID, "2030-02-01", "2030-02-07")
	input := map[string]any{"title": "参考", "url": "https://example.test", "position": 0}
	path := iterPath(env.space.ID) + "/" + iteration.ID + "/links"
	env.manager().expectError(404, "", http.MethodPost, path, input)
	var link struct {
		ID string `json:"id"`
	}
	valid := iterPath(other.ID) + "/" + iteration.ID + "/links"
	env.owner().must(201, http.MethodPost, valid, input, &link)
	env.manager().expectError(404, "", http.MethodDelete, path+"/"+link.ID, nil)
	env.exec(`UPDATE openfox.iterations SET status='canceled',canceled_at=clock_timestamp() WHERE id=$1::uuid`, iteration.ID)
	env.owner().must(201, http.MethodPost, valid, input, nil)
	env.owner().must(204, http.MethodDelete, valid+"/"+link.ID, nil, nil)
}

func TestReviewIterationLinkLimitSerializes(t *testing.T) {
	env := newS3Env(t)
	patchIterSettings(env, env.owner(), env.space.ID, map[string]any{"enabled": true, "mode": "manual"})
	iteration := createIteration(env, env.space.ID, "2030-03-01", "2030-03-07")
	path := iterPath(env.space.ID) + "/" + iteration.ID + "/links"
	for i := 0; i < 19; i++ {
		env.owner().must(201, http.MethodPost, path, map[string]any{"title": fmt.Sprint(i), "url": "https://example.test", "position": i}, nil)
	}
	statuses := parallel(2, func(i int) int {
		return env.owner().do(http.MethodPost, path, map[string]any{"title": "并发链接", "url": "https://example.test", "position": 19}).Status
	})
	if countStatus(statuses, 201) != 1 {
		t.Fatalf("并发写入越过 20 条上限：%v", statuses)
	}
}

func TestReviewEditorCannotModifySpaceOwner(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			env := newS3Env(t)
			env.addSpaceMember(env.space.ID, s3OtherID, "EDITOR")
			env.createSpace("shared")
			env.other().expectError(403, "", method, spacePath(env.space.ID)+"/members/"+s3OwnerID, map[string]any{"role": "MEMBER"})
		})
	}
}
