// Package storage 统计 Daemon 本机存储占用（心跳 capabilities.storage），只上报字节数与数量，不上报路径。
package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultMaxEntries / DefaultTimeout 是单次统计遍历的上限；超限时结果带 partial=true。
	DefaultMaxEntries = 200_000
	DefaultTimeout    = 20 * time.Second
	// MeasureInterval 是心跳存储统计的刷新周期。
	MeasureInterval = 10 * time.Minute
)

var errLimit = errors.New("storage: walk limit reached")

// Paths 是需要统计的位置。WorktreePrefix / CodexHomePrefix 用于识别目录类型。
type Paths struct {
	WorkspaceRoot   string
	WorktreePrefix  string
	CodexHomeRoot   string
	CodexHomePrefix string
	JournalPath     string
	// CacheDirs 是可清理缓存目录（消息附件、运行临时目录等），整体计入「可清理缓存」。
	CacheDirs []string
	// KindOf 返回 worktree 目录的用途（task / conversation），未知返回空串；为空时不细分。
	KindOf func(path string) string
}

// 存储类别（Stats.Categories 的 key）：按类型统计占用。
// 自动化来源由中心按运行输入识别。
const (
	CategoryCache        = "cache"
	CategoryTask         = "task"
	CategoryConversation = "conversation"
	CategoryAutomation   = "automation"
	CategoryWorkflow     = "workflow"
	CategoryOther        = "other"
)

// MaxCategories 是 Stats.Categories 的条数上限。
const MaxCategories = 8

// Category 是一类占用：Key 为上面的常量，Bytes / Count（条目数，如 worktree 数；未统计为 0）。
type Category struct {
	Key   string `json:"key"`
	Bytes int64  `json:"bytes"`
	Count int    `json:"count"`
}

// Stats 是 capabilities.storage 的结构。
type Stats struct {
	TotalBytes     int64     `json:"total_bytes"`
	WorktreeBytes  int64     `json:"worktree_bytes"`
	WorktreeCount  int       `json:"worktree_count"`
	CodexHomeBytes int64     `json:"codex_home_bytes"`
	JournalBytes   int64     `json:"journal_bytes"`
	MeasuredAt     time.Time `json:"measured_at"`
	Partial        bool      `json:"partial,omitempty"`
	// Categories 是按类型的占用细分，总和等于 TotalBytes；没有细分数据时为空。
	Categories []Category `json:"categories,omitempty"`
}

// Walker 是带条目数与时间上限的遍历器；同一个 Walker 的上限在多次 Size 调用间累计。
type Walker struct {
	MaxEntries int
	Deadline   time.Time
	entries    int
	partial    bool
}

// NewWalker 返回默认上限的遍历器。
func NewWalker(now time.Time) *Walker {
	return &Walker{MaxEntries: DefaultMaxEntries, Deadline: now.Add(DefaultTimeout)}
}

// Partial 报告是否因上限提前停止。
func (w *Walker) Partial() bool { return w.partial }

// Size 返回 root 下普通文件的总字节数，不跟随符号链接；root 不存在时为 0。
func (w *Walker) Size(ctx context.Context, root string) int64 {
	if w.partial {
		return 0
	}
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			// 无权限或并发删除的条目跳过。
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		w.entries++
		if (w.MaxEntries > 0 && w.entries > w.MaxEntries) || (!w.Deadline.IsZero() && w.entries%256 == 0 && time.Now().After(w.Deadline)) {
			return errLimit
		}
		if w.entries%256 == 0 && ctx.Err() != nil {
			return errLimit
		}
		if entry.Type().IsRegular() {
			if info, err := entry.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if errors.Is(err, errLimit) {
		w.partial = true
	}
	return total
}

// Measure 统计工作区根（含 worktree 与非仓库目录）、独立 CODEX_HOME 与 journal 文件的占用。
func Measure(ctx context.Context, paths Paths, now time.Time) Stats {
	walker := NewWalker(now)
	stats := Stats{MeasuredAt: now.UTC().Truncate(time.Second)}
	var workspaceOther int64
	byKind, countKind := map[string]int64{}, map[string]int{}
	if entries, err := os.ReadDir(paths.WorkspaceRoot); err == nil {
		for _, entry := range entries {
			path := filepath.Join(paths.WorkspaceRoot, entry.Name())
			if entry.IsDir() && paths.WorktreePrefix != "" && strings.HasPrefix(entry.Name(), paths.WorktreePrefix) {
				stats.WorktreeCount++
				size := walker.Size(ctx, path)
				stats.WorktreeBytes += size
				kind := ""
				if paths.KindOf != nil {
					kind = paths.KindOf(path)
				}
				switch kind {
				case CategoryTask, CategoryConversation, CategoryAutomation:
					byKind[kind] += size
					countKind[kind]++
				default:
					byKind[CategoryOther] += size
				}
				continue
			}
			if paths.CodexHomeRoot != "" && sameOrWithin(path, paths.CodexHomeRoot) {
				// CODEX_HOME 根在工作区根内时单独统计，避免重复计入。
				continue
			}
			workspaceOther += walker.Size(ctx, path)
		}
	}
	if paths.CodexHomeRoot != "" {
		if entries, err := os.ReadDir(paths.CodexHomeRoot); err == nil {
			for _, entry := range entries {
				if entry.IsDir() && strings.HasPrefix(entry.Name(), paths.CodexHomePrefix) {
					stats.CodexHomeBytes += walker.Size(ctx, filepath.Join(paths.CodexHomeRoot, entry.Name()))
				}
			}
		}
	}
	if paths.JournalPath != "" {
		// SQLite WAL 模式下还有 -wal / -shm 文件。
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if info, err := os.Lstat(paths.JournalPath + suffix); err == nil && info.Mode().IsRegular() {
				stats.JournalBytes += info.Size()
			}
		}
	}
	var cacheDirs int64
	for _, dir := range paths.CacheDirs {
		if dir != "" {
			cacheDirs += walker.Size(ctx, dir)
		}
	}
	stats.TotalBytes = stats.WorktreeBytes + workspaceOther + stats.CodexHomeBytes + stats.JournalBytes + cacheDirs
	if paths.KindOf != nil {
		byKind[CategoryOther] += workspaceOther + stats.JournalBytes
		stats.Categories = buildCategories(stats.CodexHomeBytes+cacheDirs, byKind, countKind)
	}
	stats.Partial = walker.Partial()
	return stats
}

// buildCategories 按固定顺序输出非零的类别（cache、task、conversation、other）。
func buildCategories(cache int64, byKind map[string]int64, count map[string]int) []Category {
	result := make([]Category, 0, 4)
	add := func(key string, bytes int64, n int) {
		if bytes > 0 && len(result) < MaxCategories {
			result = append(result, Category{Key: key, Bytes: bytes, Count: n})
		}
	}
	add(CategoryCache, cache, 0)
	add(CategoryTask, byKind["task"], count["task"])
	add(CategoryConversation, byKind["conversation"], count["conversation"])
	add(CategoryAutomation, byKind[CategoryAutomation], count[CategoryAutomation])
	add(CategoryOther, byKind[CategoryOther], 0)
	return result
}

// Within 报告 path（解析符号链接后）是否在 root（解析符号链接后）之内且不等于 root。
// 用于删除前的最后一道校验。
func Within(root, path string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(resolvedRoot, resolved)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func sameOrWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
