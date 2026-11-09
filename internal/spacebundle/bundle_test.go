package spacebundle

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func build(t *testing.T, mutate func(*Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Write(KindHeader, Header{Version: Version, SourceSpaceID: "s1", CreatedAt: time.Unix(0, 0).UTC(), Space: Space{Name: "演示", Prefix: "DEMO"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t1", "t2", "t3"} {
		if err := w.Write(KindTask, Task{ID: id, Title: "任务 " + id, Priority: "NORMAL"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Write(KindComment, Comment{ID: "c1", TaskID: "t1", Body: "第一条"}); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(w)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func opener(data []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
}

func TestRoundTripAndSelect(t *testing.T) {
	data := build(t, nil)
	h, end, err := ReadEnd(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != Version || h.Space.Prefix != "DEMO" || end.Counts[KindTask] != 3 || end.Counts[KindComment] != 1 {
		t.Fatalf("header/end 不符: %+v %+v", h, end)
	}
	raws, err := Select(opener(data), KindTask, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := Decode[Task](raws)
	if err != nil || len(tasks) != 1 || tasks[0].ID != "t2" {
		t.Fatalf("跳过 1 条取 1 条应得 t2: %+v %v", tasks, err)
	}
	raws, err = Select(opener(data), KindTask, 3, 10)
	if err != nil || len(raws) != 0 {
		t.Fatalf("跳过全部应为空: %v %v", raws, err)
	}
	raws, err = Select(opener(data), KindComment, 0, 0)
	if err != nil || len(raws) != 1 {
		t.Fatalf("评论应有 1 条: %v %v", raws, err)
	}
}

func TestUnsupportedVersion(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_ = w.Write(KindHeader, Header{Version: Version + 1})
	_ = w.Close()
	if _, _, err := ReadEnd(bytes.NewReader(buf.Bytes())); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("更高版本应被拒绝，得到 %v", err)
	}
}

func TestTruncatedWithoutEnd(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	hdr, _ := json.Marshal(Header{Version: Version})
	_, _ = gz.Write(append([]byte(KindHeader+"\t"), append(hdr, '\n')...))
	_ = gz.Close()
	if _, err := Select(opener(buf.Bytes()), KindTask, 0, 0); !errors.Is(err, ErrTruncated) {
		t.Fatalf("缺少 end 应报截断，得到 %v", err)
	}
	// 压缩流被截断。
	data := build(t, nil)
	if _, _, err := ReadEnd(bytes.NewReader(data[:len(data)/2])); err == nil {
		t.Fatal("截断的压缩流应报错")
	}
}

func TestInvalidInputs(t *testing.T) {
	if _, err := NewReader(strings.NewReader("not gzip")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("非 gzip 应为 ErrInvalid，得到 %v", err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("task\t{}\n"))
	_ = gz.Close()
	if _, _, err := ReadEnd(bytes.NewReader(buf.Bytes())); !errors.Is(err, ErrInvalid) {
		t.Fatalf("第一条不是 header 应为 ErrInvalid，得到 %v", err)
	}
}

func TestWriterSizeLimit(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.raw = MaxBytes - 10
	if err := w.Write(KindTask, Task{ID: "t", Title: "超出"}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超过解压上限应为 ErrTooLarge，得到 %v", err)
	}
}

func TestSummaryOver(t *testing.T) {
	if (Summary{Tasks: MaxTasks}).Over() != (Limits{}) {
		t.Fatal("恰好等于上限不算超出")
	}
	l := Summary{Tasks: MaxTasks + 1, Comments: MaxComments + 1, Activities: MaxActivities + 1}.Over()
	if !l.Tasks || !l.Comments || !l.Activities {
		t.Fatalf("三项都应超出: %+v", l)
	}
}
