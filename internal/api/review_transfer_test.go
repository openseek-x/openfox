package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"openfox/internal/artifacts"
	"openfox/internal/executiondelivery"
	"testing"
	"time"
)

type reviewSlowStorage struct{ artifacts.Storage }
type reviewSlowReader struct{ remaining int }

func (reviewSlowStorage) Open(context.Context, string) (io.ReadCloser, error) {
	return &reviewSlowReader{remaining: 16 << 20}, nil
}
func (r *reviewSlowReader) Close() error { return nil }
func (r *reviewSlowReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining, 32<<10)
	time.Sleep(time.Duration(n) * time.Second / (1 << 20))
	clear(p[:n])
	r.remaining -= n
	return n, nil
}
func TestReviewLargeDownloadOutlivesDefaultDeadline(t *testing.T) {
	h := &Handler{delivery: executiondelivery.Service{Storage: reviewSlowStorage{}}}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.streamStoredFile(w, r, "large", "application/octet-stream", 16<<20)
	}))
	server.Config.ReadTimeout = 15 * time.Second
	server.Config.WriteTimeout = 15 * time.Second
	server.Start()
	defer server.Close()
	client := server.Client()
	client.Timeout = 25 * time.Second
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	got, err := io.Copy(io.Discard, response.Body)
	if err != nil || got != 16<<20 {
		t.Fatalf("约 8 Mbps 的下载被截断：收到 %d 字节，错误 %v", got, err)
	}
}

type reviewCleanupStorage struct {
	artifacts.Storage
	calls    int
	err      error
	deadline bool
}

func (s *reviewCleanupStorage) Delete(ctx context.Context, _ string) error {
	s.calls++
	s.err = ctx.Err()
	_, s.deadline = ctx.Deadline()
	return s.err
}
func TestReviewCleanupSurvivesCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	storage := &reviewCleanupStorage{}
	h := &Handler{delivery: executiondelivery.Service{Storage: storage}}
	h.removeStoredFiles(ctx, []string{"刚上传的文件"})
	if storage.calls != 1 || storage.err != nil || !storage.deadline {
		t.Fatalf("失败清理继承了取消状态或没有截止时间：%d %v %v", storage.calls, storage.err, storage.deadline)
	}
}

func TestReviewArtifactCleanupChecksCommitOutcome(t *testing.T) {
	for _, scenario := range []string{"committed", "rolled_back", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			storage := &reviewCleanupStorage{}
			checked := false
			removeUnregisteredStorageObject(ctx, storage, "对象", func(check context.Context) (bool, error) {
				checked = true
				if check.Err() != nil {
					t.Fatal("提交核查使用已取消上下文")
				}
				if _, ok := check.Deadline(); !ok {
					t.Fatal("提交核查没有截止时间")
				}
				if scenario == "unknown" {
					return false, context.DeadlineExceeded
				}
				return scenario == "committed", nil
			})
			want := 0
			if scenario == "rolled_back" {
				want = 1
			}
			if !checked || storage.calls != want {
				t.Fatalf("%s 的清理未核查提交结果：checked=%v deletes=%d", scenario, checked, storage.calls)
			}
		})
	}
}
