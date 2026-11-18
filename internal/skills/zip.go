package skills

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
)

// UnzipFiles 解压 zip：条目 ≤ 100、解压后合计 ≤ 5 MB（按实际读出的字节计，不信任头部声明）、
// 拒绝符号链接与不合规路径。忽略目录条目与 macOS 打包带来的 __MACOSX/、.DS_Store。
// 去掉唯一一层根目录的处理由 NormalizeFiles 完成。
func UnzipFiles(data []byte) ([]File, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrInvalid
	}
	if len(reader.File) > MaxFiles+MaxFiles {
		// 目录条目也算条目，但数量失控的 zip 直接拒绝。
		return nil, ErrTooLarge
	}
	files := make([]File, 0, len(reader.File))
	var total int64
	for _, entry := range reader.File {
		if entry.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalid
		}
		name := entry.Name
		if strings.HasPrefix(name, "__MACOSX/") || strings.HasSuffix(name, "/.DS_Store") || name == ".DS_Store" {
			continue
		}
		if strings.HasSuffix(name, "/") {
			continue
		}
		if err := ValidatePath(name); err != nil {
			return nil, err
		}
		if len(files) >= MaxFiles {
			return nil, ErrTooLarge
		}
		content, err := readEntry(entry, MaxTotalBytes-total)
		if err != nil {
			return nil, err
		}
		total += int64(len(content))
		files = append(files, File{Path: name, Content: content})
	}
	return files, nil
}

// readEntry 最多读出 remaining 字节；超过即视为超限（zip 炸弹）。
func readEntry(entry *zip.File, remaining int64) ([]byte, error) {
	handle, err := entry.Open()
	if err != nil {
		return nil, ErrInvalid
	}
	defer handle.Close()
	content, err := io.ReadAll(io.LimitReader(handle, remaining+1))
	if err != nil {
		if errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) {
			return nil, ErrInvalid
		}
		return nil, ErrInvalid
	}
	if int64(len(content)) > remaining {
		return nil, ErrTooLarge
	}
	return content, nil
}
