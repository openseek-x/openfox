package hermes

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRootModelProxyDuplicateAuthAndBodyLimit(t *testing.T) {
	var hits atomic.Int32
	p, _ := fixtureProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }), 1024)
	for _, duplicate := range []bool{true, false} {
		body := `{"model":"fixed"}`
		if !duplicate {
			body = strings.Repeat("x", maxModelRequestBytes+1)
		}
		r, _ := http.NewRequest("POST", p.url+"/chat/completions", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+p.token)
		want := 413
		if duplicate {
			r.Header.Add("Authorization", "Bearer "+p.token)
			want = 401
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("status=%d want=%d", res.StatusCode, want)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("rejected request reached upstream")
	}
}
func TestRootModelProxyBoundsResponse(t *testing.T) {
	p, _ := fixtureProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}), 8)
	r, _ := http.NewRequest("POST", p.url+"/chat/completions", strings.NewReader(`{"model":"fixed"}`))
	r.Header.Set("Authorization", "Bearer "+p.token)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if p.Err() == nil || len(data) > 8 {
		t.Fatalf("budget not enforced bytes=%d err=%v", len(data), p.Err())
	}
}
func TestRootModelProxyRejectsThirdConcurrentRequest(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	p, _ := fixtureProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}), 1024)
	send := func() (int, error) {
		r, _ := http.NewRequest("POST", p.url+"/chat/completions", strings.NewReader(`{"model":"fixed"}`))
		r.Header.Set("Authorization", "Bearer "+p.token)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			return 0, err
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode, nil
	}
	done := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			status, err := send()
			if err != nil {
				done <- 0
			} else {
				done <- status
			}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("upstream request did not start")
		}
	}
	status, err := send()
	if err != nil || status != 429 {
		t.Fatalf("third status=%d err=%v", status, err)
	}
	once.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		select {
		case s := <-done:
			if s != 200 {
				t.Fatalf("first two status=%d", s)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("request did not finish")
		}
	}
}
