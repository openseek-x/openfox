package collaboration

// 空间估时。刻度规则、任务估时校验、估时设置修改（可选批量转换）与转换预览。
//
// 存储：tasks.estimate numeric(14,2)，点数刻度存点数，time 刻度存秒；NULL = 未估时。
// 固定档位刻度的点数：tShirt XS=1 S=2 M=3 L=5 XL=8，扩展 XXL=13 XXXL=21。

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

const (
	EstimationNotUsed     = "notUsed"
	EstimationStoryPoints = "storyPoints"
	EstimationTime        = "time"
	EstimationExponential = "exponential"
	EstimationFibonacci   = "fibonacci"
	EstimationLinear      = "linear"
	EstimationTShirt      = "tShirt"

	// MaxEstimate 与数据库约束 tasks_estimate_range 一致。
	MaxEstimate = 1e9
	// maxEstimateConversions 限制一次转换的映射条数（与契约 maxItems 一致）。
	maxEstimateConversions = 200
)

var (
	ErrEstimationDisabled = NewRuleError(http.StatusBadRequest, "ESTIMATION_DISABLED", "estimation is not enabled for this space")
	ErrInvalidEstimate    = NewRuleError(http.StatusBadRequest, "INVALID_ESTIMATE", "estimate is not valid for the space estimation scale")
	errInvalidScale       = NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "estimation scale is invalid")
)

// 固定档位刻度：基础档位与扩展档位。
var estimationLevels = map[string]struct{ base, extended []float64 }{
	EstimationExponential: {base: []float64{1, 2, 4, 8, 16}, extended: []float64{32, 64}},
	EstimationFibonacci:   {base: []float64{1, 2, 3, 5, 8}, extended: []float64{13, 21}},
	EstimationLinear:      {base: []float64{1, 2, 3, 4, 5}, extended: []float64{6, 7}},
	EstimationTShirt:      {base: []float64{1, 2, 3, 5, 8}, extended: []float64{13, 21}},
}

// ParseEstimationScale 校验刻度名（区分大小写，与契约枚举一致）。
func ParseEstimationScale(value string) (string, error) {
	switch value {
	case EstimationNotUsed, EstimationStoryPoints, EstimationTime,
		EstimationExponential, EstimationFibonacci, EstimationLinear, EstimationTShirt:
		return value, nil
	default:
		return "", errInvalidScale
	}
}

// EstimationAllowedValues 返回固定档位刻度的合法值（升序，allow_zero 时含 0）；
// storyPoints / time / notUsed 没有固定档位，返回 nil。
func EstimationAllowedValues(settings SpaceEstimation) []float64 {
	levels, ok := estimationLevels[settings.Scale]
	if !ok {
		return nil
	}
	values := make([]float64, 0, len(levels.base)+len(levels.extended)+1)
	if settings.AllowZero {
		values = append(values, 0)
	}
	values = append(values, levels.base...)
	if settings.Extended {
		values = append(values, levels.extended...)
	}
	return values
}

// validEstimateNumber：有限、非负、不超过上限、最多两位小数（存储精度）。
func validEstimateNumber(value float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > MaxEstimate {
		return false
	}
	scaled := value * 100
	return math.Abs(scaled-math.Round(scaled)) < 1e-6
}

// ValidateEstimate 校验一个非空估时值：未启用估时 → ESTIMATION_DISABLED；不符合刻度 → INVALID_ESTIMATE。
func ValidateEstimate(settings SpaceEstimation, value float64) error {
	if settings.Scale == EstimationNotUsed || settings.Scale == "" {
		return ErrEstimationDisabled
	}
	if !validEstimateNumber(value) {
		return ErrInvalidEstimate
	}
	if value == 0 {
		if settings.AllowZero {
			return nil
		}
		return ErrInvalidEstimate
	}
	switch settings.Scale {
	case EstimationStoryPoints:
		return nil
	case EstimationTime:
		if value != math.Trunc(value) {
			return ErrInvalidEstimate
		}
		return nil
	}
	for _, allowed := range EstimationAllowedValues(settings) {
		if allowed == value {
			return nil
		}
	}
	return ErrInvalidEstimate
}

// suggestEstimate 返回值在目标刻度下的建议映射：合法时为自身；固定档位取最接近的档位（距离相同取较大值）；
// 其他情况（未启用、点数 / 时间刻度下不合法）为 nil，表示建议清空。
func suggestEstimate(settings SpaceEstimation, value float64) *float64 {
	if ValidateEstimate(settings, value) == nil {
		return &value
	}
	allowed := EstimationAllowedValues(settings)
	if len(allowed) > 0 {
		best := allowed[0]
		for _, candidate := range allowed[1:] {
			if math.Abs(candidate-value) <= math.Abs(best-value) {
				best = candidate
			}
		}
		return &best
	}
	if settings.Scale == EstimationTime && value > 0 {
		rounded := math.Round(value)
		if rounded >= 1 {
			return &rounded
		}
	}
	return nil
}

// OptionalFloat 区分 JSON 中「未传」「null」「数值」三种情况。
type OptionalFloat struct {
	Set   bool
	Value *float64
}

func (o *OptionalFloat) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

// loadSpaceEstimation 读取未删除空间的估时设置；lock 为空或 "FOR KEY SHARE" / "FOR UPDATE"。
// 任务写入用 FOR KEY SHARE：与修改估时设置的 FOR UPDATE 互斥，但不阻塞建任务递增序号的 UPDATE。
func loadSpaceEstimation(ctx context.Context, tx pgx.Tx, spaceID, lock string) (SpaceEstimation, error) {
	var settings SpaceEstimation
	err := tx.QueryRow(ctx, `
		SELECT estimation_scale, estimation_extended, estimation_allow_zero, estimation_count_unestimated
		  FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND deleted_at IS NULL `+lock, spaceID).Scan(
		&settings.Scale, &settings.Extended, &settings.AllowZero, &settings.CountUnestimated)
	if errors.Is(err, pgx.ErrNoRows) {
		return SpaceEstimation{}, ErrSpaceNotFound
	}
	return settings, err
}

// EstimateConversion 是一条估时转换映射；To 为 nil 表示清空。
type EstimateConversion struct {
	From float64  `json:"from"`
	To   *float64 `json:"to"`
}

// EstimationUpdateInput 是 PATCH …/estimation 的输入。
type EstimationUpdateInput struct {
	Version          uint64
	Scale            string
	Extended         bool
	AllowZero        bool
	CountUnestimated bool
	Convert          []EstimateConversion
}

// UpdateSpaceEstimation 修改空间估时设置，并在同一事务里按映射批量改写已有估时（映射同时生效，不链式转换）。
// 只改本空间未删除任务；返回实际改写的任务数。调用方负责空间管理权限判断。
func (s Service) UpdateSpaceEstimation(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input EstimationUpdateInput) (int64, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return 0, err
	}
	if !membership.ValidateUUID(spaceID) {
		return 0, ErrSpaceNotFound
	}
	if input.Version == 0 {
		return 0, ErrInvalidVersion
	}
	scale, err := ParseEstimationScale(input.Scale)
	if err != nil {
		return 0, err
	}
	settings := SpaceEstimation{Scale: scale, Extended: input.Extended, AllowZero: input.AllowZero,
		CountUnestimated: input.CountUnestimated}
	if len(input.Convert) > maxEstimateConversions {
		return 0, invalidInput("convert supports at most 200 mappings")
	}
	froms := make([]float64, 0, len(input.Convert))
	tos := make([]*float64, 0, len(input.Convert))
	seen := make(map[float64]bool, len(input.Convert))
	for _, mapping := range input.Convert {
		if !validEstimateNumber(mapping.From) {
			return 0, invalidInput("convert.from must be a non-negative estimate")
		}
		if seen[mapping.From] {
			return 0, invalidInput("convert.from values must be unique")
		}
		seen[mapping.From] = true
		if mapping.To != nil {
			if err := ValidateEstimate(settings, *mapping.To); err != nil {
				// 新刻度未启用时也不能转换为非空值，统一报不符合刻度。
				return 0, ErrInvalidEstimate
			}
		}
		froms = append(froms, mapping.From)
		tos = append(tos, mapping.To)
	}
	var currentVersion int64
	err = tx.QueryRow(ctx, `
		SELECT version FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND deleted_at IS NULL
		   FOR UPDATE`, spaceID).Scan(&currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrSpaceNotFound
	}
	if err != nil {
		return 0, err
	}
	if uint64(currentVersion) != input.Version {
		return 0, ErrVersionConflict
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.spaces
		   SET estimation_scale = $2, estimation_extended = $3, estimation_allow_zero = $4,
		       estimation_count_unestimated = $5, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $6 AND deleted_at IS NULL`,
		spaceID, settings.Scale, settings.Extended, settings.AllowZero, settings.CountUnestimated, int64(input.Version))
	if err != nil {
		return 0, mapDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return 0, ErrVersionConflict
	}
	if len(froms) == 0 {
		return 0, nil
	}
	// 一条语句完成全部映射：每个任务最多命中一条映射（from 唯一），因此不会链式转换。
	converted, err := tx.Exec(ctx, `
		UPDATE openfox.tasks t
		   SET estimate = round(m.to_value::numeric, 2), version = t.version + 1, updated_at = clock_timestamp()
		  FROM unnest($2::float8[], $3::float8[]) AS m(from_value, to_value)
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.space_id = $1::uuid AND t.deleted_at IS NULL
		   AND t.estimate = round(m.from_value::numeric, 2)
		   AND t.estimate IS DISTINCT FROM round(m.to_value::numeric, 2)`, spaceID, froms, tos)
	if err != nil {
		return 0, mapDatabaseError(err)
	}
	return converted.RowsAffected(), nil
}

// EstimationPreviewValue 是预览中的一个已用估时值。
type EstimationPreviewValue struct {
	Value     float64  `json:"value"`
	Count     int64    `json:"count"`
	Valid     bool     `json:"valid"`
	Suggested *float64 `json:"suggested"`
}

// EstimationPreview 对应契约 SpaceEstimationPreview。
type EstimationPreview struct {
	Scale            string                   `json:"scale"`
	AllowedValues    []float64                `json:"allowed_values"`
	Values           []EstimationPreviewValue `json:"values"`
	UnestimatedCount int64                    `json:"unestimated_count"`
	TotalCount       int64                    `json:"total_count"`
}

// PreviewEstimation 统计本空间未删除任务当前用到的估时值，并按目标刻度给出合法性与建议映射。
// 调用方负责空间读权限判断。
func (s Service) PreviewEstimation(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, target SpaceEstimation) (EstimationPreview, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return EstimationPreview{}, err
	}
	if !membership.ValidateUUID(spaceID) {
		return EstimationPreview{}, ErrSpaceNotFound
	}
	scale, err := ParseEstimationScale(target.Scale)
	if err != nil {
		return EstimationPreview{}, err
	}
	target.Scale = scale
	if _, err := loadSpaceEstimation(ctx, tx, spaceID, ""); err != nil {
		return EstimationPreview{}, err
	}
	preview := EstimationPreview{Scale: scale, AllowedValues: EstimationAllowedValues(target),
		Values: make([]EstimationPreviewValue, 0)}
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE estimate IS NULL)
		  FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND deleted_at IS NULL`, spaceID).Scan(&preview.TotalCount, &preview.UnestimatedCount); err != nil {
		return EstimationPreview{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT estimate::float8, count(*)
		  FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND deleted_at IS NULL AND estimate IS NOT NULL
		 GROUP BY estimate
		 ORDER BY estimate`, spaceID)
	if err != nil {
		return EstimationPreview{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item EstimationPreviewValue
		if err := rows.Scan(&item.Value, &item.Count); err != nil {
			return EstimationPreview{}, err
		}
		item.Valid = ValidateEstimate(target, item.Value) == nil
		item.Suggested = suggestEstimate(target, item.Value)
		preview.Values = append(preview.Values, item)
	}
	if err := rows.Err(); err != nil {
		return EstimationPreview{}, err
	}
	sort.SliceStable(preview.Values, func(i, j int) bool { return preview.Values[i].Value < preview.Values[j].Value })
	return preview, nil
}

// parseEstimateBound 解析筛选参数里的估时上下界。
func parseEstimateBound(raw string) (*float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := parseFiniteFloat(raw)
	if err != nil || value < 0 || value > MaxEstimate {
		return nil, errInvalidFilter
	}
	return &value, nil
}

func parseFiniteFloat(raw string) (float64, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errInvalidFilter
	}
	return value, nil
}
