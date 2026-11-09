package stage3

import (
	"net/http"
	"sync"
	"testing"
)

func TestPreferencesDensityConcurrentWrites(t *testing.T) {
	for _, initialVersion := range []int64{0, 1} {
		t.Run(map[int64]string{0: "first save", 1: "existing preferences"}[initialVersion], func(t *testing.T) {
			env := newS3Env(t)
			user := env.newUser()
			first := env.client(user)
			second := secondClient(env, user)
			if initialVersion == 1 {
				first.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences", map[string]any{"version": 0, "theme": "dark"}, nil)
			}
			start := make(chan struct{})
			responses := make(chan s3Response, 2)
			var workers sync.WaitGroup
			for _, client := range []*s3Client{first, second} {
				workers.Add(1)
				go func() {
					defer workers.Done()
					<-start
					responses <- client.do(http.MethodPatch, "/v1/me/preferences", map[string]any{"version": initialVersion, "density": "compact"})
				}()
			}
			close(start)
			workers.Wait()
			close(responses)
			succeeded, conflicted := 0, 0
			for response := range responses {
				switch {
				case response.Status == http.StatusOK:
					succeeded++
				case response.Status == http.StatusConflict && response.code() == "VERSION_CONFLICT":
					conflicted++
				default:
					t.Fatalf("concurrent save: %d %s", response.Status, response.Body)
				}
			}
			if succeeded != 1 || conflicted != 1 {
				t.Fatalf("success=%d conflict=%d", succeeded, conflicted)
			}
			var prefs profilePrefs
			first.must(http.StatusOK, http.MethodGet, "/v1/me/preferences", nil, &prefs)
			if prefs.Density != "compact" || prefs.Version != initialVersion+1 || (initialVersion == 1 && prefs.Theme != "dark") {
				t.Fatalf("concurrent result: %+v", prefs)
			}
		})
	}
}

func TestPreferencesDensityAcrossSessions(t *testing.T) {
	env := newS3Env(t)
	user := env.newUser()
	first := env.client(user)
	second := secondClient(env, user)
	other := env.client(env.newUser())
	var prefs profilePrefs
	first.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences", map[string]any{"version": 0, "density": "compact"}, &prefs)
	if prefs.Density != "compact" || prefs.Version != 1 {
		t.Fatalf("saved: %+v", prefs)
	}
	second.must(http.StatusOK, http.MethodGet, "/v1/me/preferences", nil, &prefs)
	if prefs.Density != "compact" || prefs.Version != 1 {
		t.Fatalf("second session: %+v", prefs)
	}
	second.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "/v1/me/preferences", map[string]any{"version": 0, "density": "comfortable"})
	second.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences", map[string]any{"version": 1, "theme": "dark"}, &prefs)
	if prefs.Density != "compact" || prefs.Theme != "dark" || prefs.Version != 2 {
		t.Fatalf("old client omitted density: %+v", prefs)
	}
	for _, invalid := range []any{"", "COMPACT", "dense", " compact ", 1, true} {
		first.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, "/v1/me/preferences", map[string]any{"version": 2, "density": invalid})
	}
	first.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences", map[string]any{"version": 2, "density": nil}, &prefs)
	if prefs.Density != "compact" || prefs.Version != 3 {
		t.Fatalf("null density: %+v", prefs)
	}
	other.must(http.StatusOK, http.MethodGet, "/v1/me/preferences", nil, &prefs)
	if prefs.Density != "comfortable" || prefs.Version != 0 {
		t.Fatalf("another user: %+v", prefs)
	}
	first.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences", map[string]any{"version": 3, "density": "comfortable"}, &prefs)
	second.must(http.StatusOK, http.MethodGet, "/v1/me/preferences", nil, &prefs)
	if prefs.Density != "comfortable" || prefs.Theme != "dark" || prefs.Version != 4 {
		t.Fatalf("restored: %+v", prefs)
	}
}
