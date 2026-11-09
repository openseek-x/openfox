package devices

import (
	"encoding/json"
	"time"
)

// Storage 是心跳 capabilities.storage 的解析结果：只有字节数与数量，不含路径。
type Storage struct {
	TotalBytes     int64     `json:"total_bytes"`
	WorktreeBytes  int64     `json:"worktree_bytes"`
	WorktreeCount  int64     `json:"worktree_count"`
	CodexHomeBytes int64     `json:"codex_home_bytes"`
	JournalBytes   int64     `json:"journal_bytes"`
	MeasuredAt     time.Time `json:"measured_at"`
	Partial        bool      `json:"partial"`
	// Categories 是按类型的占用细分；设备没有上报或上报不合法时为空数组。
	Categories []StorageCategory `json:"categories"`
}

// StorageCategory 是 Storage.Categories 的一项。
type StorageCategory struct {
	Key   string `json:"key"`
	Bytes int64  `json:"bytes"`
	Count int64  `json:"count"`
}

// maxStorageCategories 是接受的类别条数上限；类别键只接受小写字母、数字与下划线，最长 32。
const maxStorageCategories = 8

func validCategoryKey(key string) bool {
	if key == "" || len(key) > 32 {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' {
			return false
		}
	}
	return true
}

// parseCategories 校验设备上报的类别：条数超限、键非法或重复、出现负数、总和超过 total 时整体丢弃
// （返回空数组），不信任部分有效的数据。
func parseCategories(raw []struct {
	Key   string `json:"key"`
	Bytes *int64 `json:"bytes"`
	Count *int64 `json:"count"`
}, total int64) []StorageCategory {
	result := []StorageCategory{}
	if len(raw) == 0 || len(raw) > maxStorageCategories {
		return result
	}
	seen := map[string]bool{}
	var sum int64
	for _, item := range raw {
		if !validCategoryKey(item.Key) || seen[item.Key] || item.Bytes == nil || *item.Bytes < 0 {
			return []StorageCategory{}
		}
		var count int64
		if item.Count != nil {
			if *item.Count < 0 {
				return []StorageCategory{}
			}
			count = *item.Count
		}
		seen[item.Key] = true
		sum += *item.Bytes
		if sum < 0 || sum > total {
			return []StorageCategory{}
		}
		result = append(result, StorageCategory{Key: item.Key, Bytes: *item.Bytes, Count: count})
	}
	return result
}

// ParseStorage 读取 capabilities.storage；缺失、类型不符、出现负数或缺 measured_at 时返回 nil。
// capabilities 是设备上报的不透明 JSON，这里只接受形状正确的统计，不信任其他字段。
func ParseStorage(capabilities json.RawMessage) *Storage {
	var document struct {
		Storage *struct {
			TotalBytes     *int64     `json:"total_bytes"`
			WorktreeBytes  *int64     `json:"worktree_bytes"`
			WorktreeCount  *int64     `json:"worktree_count"`
			CodexHomeBytes *int64     `json:"codex_home_bytes"`
			JournalBytes   *int64     `json:"journal_bytes"`
			MeasuredAt     *time.Time `json:"measured_at"`
			Partial        bool       `json:"partial"`
			Categories     []struct {
				Key   string `json:"key"`
				Bytes *int64 `json:"bytes"`
				Count *int64 `json:"count"`
			} `json:"categories"`
		} `json:"storage"`
	}
	if len(capabilities) == 0 || json.Unmarshal(capabilities, &document) != nil || document.Storage == nil {
		return nil
	}
	raw := document.Storage
	values := []*int64{raw.TotalBytes, raw.WorktreeBytes, raw.WorktreeCount, raw.CodexHomeBytes, raw.JournalBytes}
	for _, value := range values {
		if value == nil || *value < 0 {
			return nil
		}
	}
	if raw.MeasuredAt == nil || raw.MeasuredAt.IsZero() {
		return nil
	}
	return &Storage{
		TotalBytes: *raw.TotalBytes, WorktreeBytes: *raw.WorktreeBytes, WorktreeCount: *raw.WorktreeCount,
		CodexHomeBytes: *raw.CodexHomeBytes, JournalBytes: *raw.JournalBytes,
		MeasuredAt: raw.MeasuredAt.UTC(), Partial: raw.Partial,
		Categories: parseCategories(raw.Categories, *raw.TotalBytes),
	}
}

// parseRuntimesCheckedAt 返回 capabilities.runtimes.*.checked_at 的最大值；都没有时返回 nil。
func parseRuntimesCheckedAt(capabilities json.RawMessage) *time.Time {
	var document struct {
		Runtimes map[string]json.RawMessage `json:"runtimes"`
	}
	if len(capabilities) == 0 || json.Unmarshal(capabilities, &document) != nil {
		return nil
	}
	var latest *time.Time
	for _, raw := range document.Runtimes {
		var item struct {
			CheckedAt *time.Time `json:"checked_at"`
		}
		if json.Unmarshal(raw, &item) != nil || item.CheckedAt == nil || item.CheckedAt.IsZero() {
			continue
		}
		if latest == nil || item.CheckedAt.After(*latest) {
			value := item.CheckedAt.UTC()
			latest = &value
		}
	}
	return latest
}
