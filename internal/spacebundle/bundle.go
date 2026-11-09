// Package spacebundle 把一个空间导出为「快照包」：gzip 压缩的行文件，每行 `<kind>\t<json>`。
//
// （克隆空间到另一个组织）先读源组织、写出快照包，worker 之后只读快照包，不再访问源组织；
// （导出任务数据）会把同一份读取器用于用户下载，所以格式带版本号（Version），并且读取与写入分开。
//
// 文件结构（顺序固定，读取方可以按 kind 过滤并跳过）：
//
//	header → status* → type* → label* → field* → project* → reltype* → task* → relation* → comment* → activity* → person* → end
//
// end 记录携带各类记录的数量，缺少 end 说明文件被截断（ErrTruncated）。
package spacebundle

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Version 是当前快照包格式版本。读取方遇到更大的版本拒绝解析（ErrUnsupportedVersion）。
const Version = 1

// 上限。MaxBytes 是解压后的字节数，防止压缩炸弹；压缩后的大小由存储层（artifacts.MaxArtifactBytes）限制。
const (
	MaxTasks      = 20000
	MaxComments   = 100000
	MaxActivities = 300000
	MaxBytes      = 200 << 20
	// maxLineBytes 是单行上限（一条评论正文最大 65000 字节，留足 JSON 转义余量）。
	maxLineBytes = 1 << 20
)

var (
	ErrUnsupportedVersion = errors.New("spacebundle: unsupported version")
	ErrTruncated          = errors.New("spacebundle: bundle is truncated")
	ErrTooLarge           = errors.New("spacebundle: bundle exceeds size limit")
	ErrInvalid            = errors.New("spacebundle: invalid bundle")
)

// 记录类型。
const (
	KindHeader   = "header"
	KindStatus   = "status"
	KindType     = "type"
	KindLabel    = "label"
	KindField    = "field"
	KindProject  = "project"
	KindRelType  = "reltype"
	KindTask     = "task"
	KindRelation = "relation"
	KindComment  = "comment"
	KindActivity = "activity"
	KindPerson   = "person"
	KindEnd      = "end"
)

// Header 是第一条记录。
type Header struct {
	Version            int       `json:"version"`
	SourceOrganization string    `json:"source_organization_id"`
	SourceOrgName      string    `json:"source_organization_name"`
	SourceSpaceID      string    `json:"source_space_id"`
	CreatedAt          time.Time `json:"created_at"`
	Space              Space     `json:"space"`
}

// Space 是空间基本信息与设置。
type Space struct {
	Name          string `json:"name"`
	Prefix        string `json:"prefix"`
	Visibility    string `json:"visibility"`
	Icon          string `json:"icon,omitempty"`
	Purpose       string `json:"purpose"`
	ViewsEnabled  bool   `json:"views_enabled"`
	ProjectsOn    bool   `json:"projects_enabled"`
	MultiAssignee bool   `json:"multi_assignee"`
	// 估点设置。
	EstimationScale         string `json:"estimation_scale"`
	EstimationExtended      bool   `json:"estimation_extended"`
	EstimationAllowZero     bool   `json:"estimation_allow_zero"`
	EstimationCountUnscored bool   `json:"estimation_count_unestimated"`
	NextTaskNumber          int64  `json:"next_task_number"`
	HasIterations           bool   `json:"iterations_enabled"`
	HasTriage               bool   `json:"triage_enabled"`
	IterationTimezone       string `json:"iteration_timezone,omitempty"`
}

// Status 是任务状态（组织级，按名称 + 类别匹配）。
type Status struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Color    string `json:"color,omitempty"`
	Icon     string `json:"icon,omitempty"`
}

// WorkflowStatus 是任务类型工作流里的一个状态。
type WorkflowStatus struct {
	StatusID  string `json:"status_id"`
	Position  int    `json:"position"`
	IsDefault bool   `json:"is_default"`
}

// Type 是任务类型及其工作流、字段。
type Type struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Icon        string           `json:"icon,omitempty"`
	Enabled     bool             `json:"enabled"`
	IsDefault   bool             `json:"is_default"`
	Archived    bool             `json:"archived,omitempty"`
	Workflow    []WorkflowStatus `json:"workflow"`
	FieldIDs    []string         `json:"field_ids,omitempty"`
}

// Label 是空间标签（分组不带，克隆后是平铺标签）。
type Label struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
}

// FieldOption 是自定义字段的选项。
type FieldOption struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Color    string `json:"color"`
	Position int    `json:"position"`
}

// Field 是自定义字段定义（组织级，按名称 + 类型匹配）。
type Field struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	Description string          `json:"description,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
	MemberScope string          `json:"member_scope,omitempty"`
	Options     []FieldOption   `json:"options,omitempty"`
}

// Project 是被任务引用的专项（按名称匹配，没有就新建）。
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Icon        string `json:"icon,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Priority    string `json:"priority"`
}

// RelType 是自定义关联类型（组织级，按名称匹配）。
type RelType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Outward string `json:"outward_label"`
	Inward  string `json:"inward_label"`
}

// Assignee 是任务处理人。Kind 为 HUMAN / AGENT / CREW；人员用 Person.Key 引用，Agent / 小队只留名称。
type Assignee struct {
	Kind      string `json:"kind"`
	PersonKey string `json:"person_key,omitempty"`
	Name      string `json:"name,omitempty"`
}

// Task 是任务。日期为 YYYY-MM-DD。
type Task struct {
	ID          string     `json:"id"`
	Sequence    int64      `json:"sequence"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Acceptance  string     `json:"acceptance,omitempty"`
	Priority    string     `json:"priority"`
	TypeID      string     `json:"type_id"`
	StatusID    string     `json:"status_id"`
	ParentID    string     `json:"parent_id,omitempty"`
	ProjectID   string     `json:"project_id,omitempty"`
	StartDate   string     `json:"start_date,omitempty"`
	DueDate     string     `json:"due_date,omitempty"`
	Estimate    *float64   `json:"estimate,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LabelIDs    []string   `json:"label_ids,omitempty"`
	Assignees   []Assignee `json:"assignees,omitempty"`
	External    string     `json:"external_assignee,omitempty"` // 外部账号的人员键
	Followers   []string   `json:"followers,omitempty"`         // 人员键
	// 以下两项只在导出任务数据（ExportOptions.Extras）时写入，克隆不使用；旧读取器会忽略未知字段，所以不升级 Version。
	IterationName string                     `json:"iteration,omitempty"`
	CreatedByKey  string                     `json:"created_by_key,omitempty"` // 创建人（人员键，取自 task.created 动态）
	Fields        map[string]json.RawMessage `json:"fields,omitempty"`
}

// Relation 是同一空间内两个任务的关联。Builtin 与 TypeID 二选一。
type Relation struct {
	SourceTaskID string `json:"source_task_id"`
	TargetTaskID string `json:"target_task_id"`
	Builtin      string `json:"builtin,omitempty"`
	TypeID       string `json:"type_id,omitempty"`
}

// Comment 是评论。AuthorKey 为人员键；Agent 作者只留 AgentName。
type Comment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	AuthorKey string    `json:"author_key,omitempty"`
	AgentName string    `json:"agent_name,omitempty"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Activity 是任务动态（只读历史）。Diff 不带：安全差异里可能含源组织的成员 ID，克隆只保留类型、操作者与时间。
type Activity struct {
	TaskID    string    `json:"task_id"`
	ActorKey  string    `json:"actor_key,omitempty"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

// Person 是被引用的人员（Key 在包内唯一）。Kind 为 user / agent / external。
type Person struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// End 是最后一条记录，带各类记录数量。
type End struct {
	Counts map[string]int `json:"counts"`
}

// ---------- 写入 ----------

// Writer 把记录写成 gzip 行文件。写入的解压后字节数超过 MaxBytes 时返回 ErrTooLarge。
type Writer struct {
	gz     *gzip.Writer
	raw    int64
	counts map[string]int
	closed bool
}

// NewWriter 包装 dst。调用方负责在 Close 之后关闭 dst。
func NewWriter(dst io.Writer) *Writer {
	return &Writer{gz: gzip.NewWriter(dst), counts: map[string]int{}}
}

// Counts 返回已写入的各类记录数量（不含 header / end）。
func (w *Writer) Counts() map[string]int {
	out := make(map[string]int, len(w.counts))
	for k, v := range w.counts {
		out[k] = v
	}
	return out
}

// Write 写一条记录。
func (w *Writer) Write(kind string, v any) error {
	if w.closed {
		return errors.New("spacebundle: writer is closed")
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	line := make([]byte, 0, len(kind)+len(payload)+2)
	line = append(line, kind...)
	line = append(line, '\t')
	line = append(line, payload...)
	line = append(line, '\n')
	if len(line) > maxLineBytes {
		return fmt.Errorf("%w: record too large", ErrTooLarge)
	}
	w.raw += int64(len(line))
	if w.raw > MaxBytes {
		return ErrTooLarge
	}
	if _, err := w.gz.Write(line); err != nil {
		return err
	}
	if kind != KindHeader && kind != KindEnd {
		w.counts[kind]++
	}
	return nil
}

// Close 写入 end 记录并结束 gzip 流。
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	if err := w.Write(KindEnd, End{Counts: w.Counts()}); err != nil {
		return err
	}
	w.closed = true
	return w.gz.Close()
}

// ---------- 读取 ----------

// Reader 顺序读取快照包。
type Reader struct {
	gz  *gzip.Reader
	br  *bufio.Reader
	raw int64
}

// NewReader 打开 gzip 行文件。
func NewReader(src io.Reader) (*Reader, error) {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return &Reader{gz: gz, br: bufio.NewReaderSize(gz, 64<<10)}, nil
}

// Next 返回下一条记录；到达文件末尾（end 之后）返回 io.EOF，没有 end 就结束返回 ErrTruncated。
func (r *Reader) Next() (kind string, payload []byte, err error) {
	line, err := r.br.ReadBytes('\n')
	r.raw += int64(len(line))
	if r.raw > MaxBytes {
		return "", nil, ErrTooLarge
	}
	if len(line) > maxLineBytes {
		return "", nil, ErrTooLarge
	}
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return "", nil, io.EOF
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "", nil, ErrTruncated
		}
		return "", nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	line = line[:len(line)-1]
	tab := bytes.IndexByte(line[:min(len(line), 16)], '\t')
	if tab <= 0 {
		return "", nil, ErrInvalid
	}
	return string(line[:tab]), line[tab+1:], nil
}

// Close 关闭 gzip 读取器。
func (r *Reader) Close() error { return r.gz.Close() }

// ReadHeader 读第一条记录并校验版本。
func (r *Reader) ReadHeader() (Header, error) {
	kind, payload, err := r.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Header{}, ErrTruncated
		}
		return Header{}, err
	}
	if kind != KindHeader {
		return Header{}, ErrInvalid
	}
	var h Header
	if err := json.Unmarshal(payload, &h); err != nil {
		return Header{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if h.Version < 1 || h.Version > Version {
		return Header{}, fmt.Errorf("%w: %d", ErrUnsupportedVersion, h.Version)
	}
	return h, nil
}

// Select 打开一份快照包，跳过前 skip 条 kind 类记录，返回接下来最多 limit 条（limit<=0 表示全部）。
// 其它类型的行只做行扫描，不解析 JSON。读到 end 之前文件结束返回 ErrTruncated。
func Select(open func() (io.ReadCloser, error), kind string, skip, limit int) ([]json.RawMessage, error) {
	src, err := open()
	if err != nil {
		return nil, err
	}
	defer src.Close()
	r, err := NewReader(src)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if _, err := r.ReadHeader(); err != nil {
		return nil, err
	}
	var out []json.RawMessage
	seen := 0
	for {
		k, payload, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil, ErrTruncated // 没有读到 end 就结束
		}
		if err != nil {
			return nil, err
		}
		if k == KindEnd {
			return out, nil
		}
		if k != kind {
			continue
		}
		seen++
		if seen <= skip {
			continue
		}
		out = append(out, json.RawMessage(append([]byte(nil), payload...)))
		if limit > 0 && len(out) >= limit {
			return out, nil
		}
	}
}

// Decode 把 Select 返回的记录解码为 T。
func Decode[T any](raws []json.RawMessage) ([]T, error) {
	out := make([]T, 0, len(raws))
	for _, raw := range raws {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// ReadEnd 读完整个包并返回 end 记录（校验完整性）。
func ReadEnd(src io.Reader) (Header, End, error) {
	r, err := NewReader(src)
	if err != nil {
		return Header{}, End{}, err
	}
	defer r.Close()
	h, err := r.ReadHeader()
	if err != nil {
		return Header{}, End{}, err
	}
	for {
		k, payload, err := r.Next()
		if errors.Is(err, io.EOF) {
			return h, End{}, ErrTruncated
		}
		if err != nil {
			return h, End{}, err
		}
		if k == KindEnd {
			var e End
			if err := json.Unmarshal(payload, &e); err != nil {
				return h, End{}, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			return h, e, nil
		}
	}
}

// Collect 一次扫描收集指定类型的全部记录（用于体量小的配置类记录，如状态 / 类型 / 字段 / 人员）。
// 没有读到 end 就结束返回 ErrTruncated。
func Collect(open func() (io.ReadCloser, error), kinds ...string) (Header, map[string][]json.RawMessage, error) {
	src, err := open()
	if err != nil {
		return Header{}, nil, err
	}
	defer src.Close()
	r, err := NewReader(src)
	if err != nil {
		return Header{}, nil, err
	}
	defer r.Close()
	h, err := r.ReadHeader()
	if err != nil {
		return Header{}, nil, err
	}
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	out := map[string][]json.RawMessage{}
	for {
		k, payload, err := r.Next()
		if errors.Is(err, io.EOF) {
			return h, nil, ErrTruncated
		}
		if err != nil {
			return h, nil, err
		}
		if k == KindEnd {
			return h, out, nil
		}
		if want[k] {
			out[k] = append(out[k], json.RawMessage(append([]byte(nil), payload...)))
		}
	}
}
