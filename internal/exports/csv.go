package exports

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"openfox/internal/spacebundle"
)

// utf8BOM 让 Excel 把文件识别为 UTF-8。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// SanitizeCell 防 CSV 公式注入：以 = + - @ 制表符 回车开头的单元格前加单引号（OWASP 建议做法）。
func SanitizeCell(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	}
	return value
}

// 固定列（顺序即文件列顺序），之后是自定义字段各一列。
var fixedColumns = []string{
	"编号", "标题", "类型", "状态", "状态类别", "优先级", "处理人", "标签", "专项", "迭代",
	"父任务编号", "截止日期", "估时", "创建人", "创建时间", "更新时间", "描述",
}

var categoryNames = map[string]string{
	"BACKLOG": "待规划", "TODO": "待办", "IN_PROGRESS": "进行中", "DONE": "已完成", "CANCELED": "已取消",
}

// WriteCSV 把一份快照包（单个空间）转成 CSV：UTF-8 带 BOM，每个任务一行。
// open 每次返回一个新的读取流（包要读两遍：先收集配置与父任务编号，再逐行输出任务）。
func WriteCSV(dst io.Writer, open func() (io.ReadCloser, error)) (tasks int, err error) {
	header, recs, err := spacebundle.Collect(open,
		spacebundle.KindStatus, spacebundle.KindType, spacebundle.KindLabel, spacebundle.KindField,
		spacebundle.KindProject, spacebundle.KindPerson)
	if err != nil {
		return 0, err
	}
	statuses, err := spacebundle.Decode[spacebundle.Status](recs[spacebundle.KindStatus])
	if err != nil {
		return 0, err
	}
	types, err := spacebundle.Decode[spacebundle.Type](recs[spacebundle.KindType])
	if err != nil {
		return 0, err
	}
	labels, err := spacebundle.Decode[spacebundle.Label](recs[spacebundle.KindLabel])
	if err != nil {
		return 0, err
	}
	fields, err := spacebundle.Decode[spacebundle.Field](recs[spacebundle.KindField])
	if err != nil {
		return 0, err
	}
	projects, err := spacebundle.Decode[spacebundle.Project](recs[spacebundle.KindProject])
	if err != nil {
		return 0, err
	}
	people, err := spacebundle.Decode[spacebundle.Person](recs[spacebundle.KindPerson])
	if err != nil {
		return 0, err
	}
	statusByID := map[string]spacebundle.Status{}
	for _, s := range statuses {
		statusByID[s.ID] = s
	}
	typeName := map[string]string{}
	for _, y := range types {
		typeName[y.ID] = y.Name
	}
	labelName := map[string]string{}
	for _, l := range labels {
		labelName[l.ID] = l.Name
	}
	projectName := map[string]string{}
	for _, p := range projects {
		projectName[p.ID] = p.Name
	}
	personByKey := map[string]spacebundle.Person{}
	for _, p := range people {
		personByKey[p.Key] = p
	}
	// 自定义字段列：按名称排序，保证列顺序稳定。
	sort.SliceStable(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })

	// 第一遍：任务编号（父任务编号需要）。只解码 id / sequence，不保留任务内容。
	sequence, err := taskSequences(open)
	if err != nil {
		return 0, err
	}

	if _, err := dst.Write(utf8BOM); err != nil {
		return 0, err
	}
	w := csv.NewWriter(dst)
	row := make([]string, 0, len(fixedColumns)+len(fields))
	row = append(row, fixedColumns...)
	for _, f := range fields {
		row = append(row, SanitizeCell(f.Name))
	}
	if err := w.Write(row); err != nil {
		return 0, err
	}

	prefix := header.Space.Prefix
	number := func(seq int64) string { return prefix + "-" + strconv.FormatInt(seq, 10) }
	personLabel := func(key string) string {
		p, ok := personByKey[key]
		if !ok {
			return ""
		}
		if p.Email != "" {
			return p.Email
		}
		return p.Name
	}

	err = eachTask(open, func(t spacebundle.Task) error {
		st := statusByID[t.StatusID]
		var assignees []string
		for _, a := range t.Assignees {
			if a.Kind == "HUMAN" {
				if v := personLabel(a.PersonKey); v != "" {
					assignees = append(assignees, v)
				}
			} else if a.Name != "" {
				assignees = append(assignees, a.Name)
			}
		}
		if t.External != "" {
			if v := personLabel(t.External); v != "" {
				assignees = append(assignees, v)
			}
		}
		var tags []string
		for _, id := range t.LabelIDs {
			if n := labelName[id]; n != "" {
				tags = append(tags, n)
			}
		}
		parent := ""
		if t.ParentID != "" {
			if seq, ok := sequence[t.ParentID]; ok {
				parent = number(seq)
			}
		}
		estimate := ""
		if t.Estimate != nil {
			estimate = strconv.FormatFloat(*t.Estimate, 'f', -1, 64)
		}
		category := categoryNames[st.Category]
		if category == "" {
			category = st.Category
		}
		row = row[:0]
		row = append(row,
			number(t.Sequence), t.Title, typeName[t.TypeID], st.Name, category, priorityLabel(t.Priority),
			strings.Join(assignees, ";"), strings.Join(tags, ";"), projectName[t.ProjectID], t.IterationName,
			parent, t.DueDate, estimate, personLabel(t.CreatedByKey),
			t.CreatedAt.UTC().Format(time.RFC3339), t.UpdatedAt.UTC().Format(time.RFC3339), t.Description)
		for _, f := range fields {
			row = append(row, formatFieldValue(f, t.Fields[f.ID]))
		}
		for i := range row {
			row[i] = SanitizeCell(row[i])
		}
		if err := w.Write(row); err != nil {
			return err
		}
		tasks++
		return nil
	})
	if err != nil {
		return tasks, err
	}
	w.Flush()
	return tasks, w.Error()
}

// eachTask 顺序扫描一遍快照包，对每个任务调用 fn（读到 end 之前结束视为截断）。
func eachTask(open func() (io.ReadCloser, error), fn func(spacebundle.Task) error) error {
	src, err := open()
	if err != nil {
		return err
	}
	defer src.Close()
	r, err := spacebundle.NewReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.ReadHeader(); err != nil {
		return err
	}
	for {
		kind, payload, err := r.Next()
		if errors.Is(err, io.EOF) {
			return spacebundle.ErrTruncated
		}
		if err != nil {
			return err
		}
		switch kind {
		case spacebundle.KindEnd:
			return nil
		case spacebundle.KindTask:
			var t spacebundle.Task
			if err := json.Unmarshal(payload, &t); err != nil {
				return fmt.Errorf("%w: %v", spacebundle.ErrInvalid, err)
			}
			if err := fn(t); err != nil {
				return err
			}
		}
	}
}

// taskSequences 返回任务 ID 到票号的映射。
func taskSequences(open func() (io.ReadCloser, error)) (map[string]int64, error) {
	out := map[string]int64{}
	err := eachTask(open, func(t spacebundle.Task) error {
		out[t.ID] = t.Sequence
		return nil
	})
	return out, err
}

// formatFieldValue 把自定义字段取值转成文本：选项换成标签，数组用分号连接。
func formatFieldValue(f spacebundle.Field, raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	label := func(v string) string {
		for _, o := range f.Options {
			if o.ID == v {
				return o.Label
			}
		}
		return v
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return label(s)
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err == nil {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			if str, ok := item.(string); ok {
				parts = append(parts, label(str))
			} else {
				parts = append(parts, fmt.Sprint(item))
			}
		}
		return strings.Join(parts, ";")
	}
	var any1 any
	if err := json.Unmarshal(raw, &any1); err == nil {
		return fmt.Sprint(any1)
	}
	return string(raw)
}

// priorityNames 是 CSV 里的优先级显示名（与界面一致：无 / 低 / 中 / 高 / 紧急）；未知值原样输出。
var priorityNames = map[string]string{"NONE": "无", "LOW": "低", "NORMAL": "中", "HIGH": "高", "URGENT": "紧急"}

func priorityLabel(priority string) string {
	if name, ok := priorityNames[priority]; ok {
		return name
	}
	return priority
}
