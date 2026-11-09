package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"

	"openfox/internal/artifacts"
	"openfox/internal/collaboration"
)

// StorageKey 返回文件的存储键：skills/<组织>/<技能>/<随机 UUID>。
// 每个文件记录独占一个对象（不按内容去重），sha256 只记在 skill_files 里：
// 删除旧文件时不会误删别的记录（或并发保存刚写入的内容）正在引用的对象。
func StorageKey(organizationID, skillID, objectID string) string {
	return "skills/" + organizationID + "/" + skillID + "/" + objectID
}

// Digest 返回内容的 sha256（十六进制）。
func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// NewEntry 计算内容的文件记录（不写存储）；存储键是新生成的随机 UUID。
func NewEntry(organizationID, skillID, path string, content []byte) (Entry, error) {
	objectID, err := collaboration.NewID()
	if err != nil {
		return Entry{}, err
	}
	return Entry{Path: path, Key: StorageKey(organizationID, skillID, objectID), Size: int64(len(content)), SHA256: Digest(content)}, nil
}

// StoreFiles 把文件写入存储，返回文件记录与本次新建的存储键（失败或事务回滚时用 DeleteKeys 清理）。
// 每个文件写入自己的新对象；出错时已写入的对象会被清理。
func StoreFiles(ctx context.Context, storage artifacts.Storage, organizationID, skillID string, files []File) ([]Entry, []string, error) {
	entries := make([]Entry, 0, len(files))
	var created []string
	for _, file := range files {
		entry, err := NewEntry(organizationID, skillID, file.Path, file.Content)
		if err == nil {
			err = putBlob(ctx, storage, entry.Key, file.Content)
		}
		if err != nil {
			DeleteKeys(ctx, storage, created)
			return nil, nil, err
		}
		created = append(created, entry.Key)
		entries = append(entries, entry)
	}
	return entries, created, nil
}

// putBlob 写入一个新对象。
func putBlob(ctx context.Context, storage artifacts.Storage, key string, content []byte) error {
	// 空文件也要能写入：Put 的上限必须大于 0。
	_, err := storage.Put(ctx, key, bytes.NewReader(content), int64(len(content))+1)
	return err
}

// DeleteKeys 尽力删除存储对象；失败只留下孤儿文件，不影响结果。
func DeleteKeys(ctx context.Context, storage artifacts.Storage, keys []string) {
	if storage == nil {
		return
	}
	for _, key := range keys {
		_ = storage.Delete(ctx, key)
	}
}

// ReadBlob 读出一个文件的内容并核对大小与 sha256（存储被改动时返回错误）。
func ReadBlob(ctx context.Context, storage artifacts.Storage, entry Entry) ([]byte, error) {
	reader, err := storage.Open(ctx, entry.Key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, entry.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != entry.Size || Digest(content) != entry.SHA256 {
		return nil, errors.New("skills: stored file does not match its record")
	}
	return content, nil
}

// BundleFile 是 DeviceSkillFile。
type BundleFile struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
}

// Bundle 是 DeviceSkillBundle（下发给设备）。
type Bundle struct {
	Name  string       `json:"name"`
	Files []BundleFile `json:"files"`
}

// ReadBundle 读出技能的全部文件，打成设备下载的技能包。
func ReadBundle(ctx context.Context, storage artifacts.Storage, name string, entries []Entry) (Bundle, error) {
	bundle := Bundle{Name: name, Files: make([]BundleFile, 0, len(entries))}
	for _, entry := range entries {
		content, err := ReadBlob(ctx, storage, entry)
		if err != nil {
			return Bundle{}, err
		}
		bundle.Files = append(bundle.Files, BundleFile{Path: entry.Path, ContentBase64: base64.StdEncoding.EncodeToString(content)})
	}
	return bundle, nil
}

// DecodeBundleFiles 把设备上传的 base64 文件还原为内存文件；按解码后的大小累计，超限立即停止。
func DecodeBundleFiles(files []BundleFile) ([]File, error) {
	if len(files) > MaxFiles {
		return nil, ErrTooLarge
	}
	result := make([]File, 0, len(files))
	var total int64
	for _, file := range files {
		if err := ValidatePath(file.Path); err != nil {
			return nil, err
		}
		if int64(base64.StdEncoding.DecodedLen(len(file.ContentBase64))) > MaxTotalBytes-total+3 {
			return nil, ErrTooLarge
		}
		content, err := base64.StdEncoding.DecodeString(file.ContentBase64)
		if err != nil {
			return nil, ErrInvalid
		}
		total += int64(len(content))
		if total > MaxTotalBytes {
			return nil, ErrTooLarge
		}
		result = append(result, File{Path: file.Path, Content: content})
	}
	return result, nil
}
