package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoveryCacheRechecksSelectionAndFileIdentity(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache.json")
	executable := filepath.Join(root, "bin")
	now := time.Now().UTC()
	if err := os.WriteFile(executable, []byte("version one"), 0700); err != nil {
		t.Fatal(err)
	}
	entry := DiscoveryEntry{Path: executable, Source: "path", Version: "1.0.0"}
	if err := WriteDiscoveryCache(cache, "profile", now, map[string]DiscoveryEntry{"pi": entry}); err != nil {
		t.Fatal(err)
	}
	items, checked := ReadDiscoveryCache(cache, "profile", now.Add(71*time.Hour))
	if items["pi"].Version != entry.Version || !checked.Equal(now) {
		t.Fatalf("保存的版本或时间错误：%+v %v", items, checked)
	}
	if items, _ := ReadDiscoveryCache(cache, "profile", now.Add(72*time.Hour)); len(items) != 0 {
		t.Fatal("过期缓存应失效")
	}
	if items, _ := ReadDiscoveryCache(cache, "changed", now); len(items) != 0 {
		t.Fatal("搜索环境变化后应重新探测")
	}
	if err := os.WriteFile(executable, []byte("changed version"), 0700); err != nil {
		t.Fatal(err)
	}
	if items, _ := ReadDiscoveryCache(cache, "profile", now); len(items) != 0 {
		t.Fatal("替换可执行文件后应重新探测")
	}
}
