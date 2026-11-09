package imports

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// maxRowErrors 是保留的行级错误上限（与契约 ImportPreview.row_errors 一致）。
const maxRowErrors = 20

// InvalidFileReason 取出 ErrInvalidFile 包装错误里的中文原因（用作 400 的 message）；不是该错误时返回空串。
func InvalidFileReason(err error) string {
	if !errors.Is(err, ErrInvalidFile) {
		return ""
	}
	return strings.TrimPrefix(err.Error(), ErrInvalidFile.Error()+": ")
}

func invalidFile(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidFile, fmt.Sprintf(format, args...))
}

// ParseJiraCSV 解析 Jira 导出的 CSV：去 UTF-8 BOM，要求合法 UTF-8；第一行是表头。
// 全空白行计入 SkippedRows；短行补空串，长行截断并记一条 RowError。
func ParseJiraCSV(r io.Reader) (ParsedFile, error) {
	// 调用方已限制体积；这里再兜一层，避免被误用时无界读取。
	data, err := io.ReadAll(io.LimitReader(r, MaxFileBytes+1))
	if err != nil {
		return ParsedFile{}, invalidFile("读取文件失败")
	}
	if len(data) > MaxFileBytes {
		return ParsedFile{}, invalidFile("文件超过 10 MiB")
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		return ParsedFile{}, invalidFile("文件不是 UTF-8 编码，请用 UTF-8 重新导出")
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.ReuseRecord = false

	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return ParsedFile{}, invalidFile("文件是空的")
	}
	if err != nil {
		return ParsedFile{}, invalidFile("无法解析表头：%s", err.Error())
	}
	if len(header) > MaxColumns {
		return ParsedFile{}, invalidFile("列数超过上限 %d", MaxColumns)
	}
	for _, cell := range header {
		if len(cell) > MaxCellBytes {
			return ParsedFile{}, invalidFile("表头单元格超过 64 KiB")
		}
	}
	if allBlank(header) {
		return ParsedFile{}, invalidFile("第一行表头是空的")
	}
	file := ParsedFile{Headers: header, RowErrors: []RowError{}}
	columns := len(header)
	// 上一条记录结束后下一行的行号，用来识别 csv 包自动跳过的纯空行。
	nextLine := 2
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := reader.FieldPos(0)
		if err != nil {
			return ParsedFile{}, invalidFile("第 %d 行 CSV 格式错误：%s", line, err.Error())
		}
		if line > nextLine {
			file.SkippedRows += line - nextLine
		}
		consumed := 1
		for _, cell := range record {
			consumed += strings.Count(cell, "\n")
			if len(cell) > MaxCellBytes {
				return ParsedFile{}, invalidFile("第 %d 行存在超过 64 KiB 的单元格", line)
			}
		}
		nextLine = line + consumed
		if allBlank(record) {
			file.SkippedRows++
			continue
		}
		if len(file.Rows) >= MaxDataRows {
			return ParsedFile{}, invalidFile("数据行数超过上限 %d 行，请拆分文件", MaxDataRows)
		}
		cells := make([]string, columns)
		copy(cells, record)
		if len(record) > columns {
			if len(file.RowErrors) < maxRowErrors {
				file.RowErrors = append(file.RowErrors, RowError{Row: line,
					Message: fmt.Sprintf("该行有 %d 个单元格，多于表头的 %d 列，多出的内容已忽略", len(record), columns)})
			}
		}
		file.Rows = append(file.Rows, ParsedRow{Line: line, Cells: cells})
	}
	return file, nil
}

func allBlank(cells []string) bool {
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}
