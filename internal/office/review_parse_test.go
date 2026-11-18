package office

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReviewInterleavedXMLTextIsBounded(t *testing.T) {
	data := []byte("<root>" + strings.Repeat(strings.Repeat("x", 256)+"<child/>", 10000) + "</root>")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	root, err := parseXML(data)
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.text) != 256*10000 {
		t.Fatalf("文本丢失：%d", len(root.text))
	}
	if elapsed > 3*time.Second {
		t.Errorf("解析超过 3 秒：%s", elapsed)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 128<<20 {
		t.Errorf("2.6 MB XML 分配了 %d MB，存在重复复制", allocated>>20)
	}
}
