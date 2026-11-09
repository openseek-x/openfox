package collaboration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

var (
	ErrIterationAutomatic    = NewRuleError(http.StatusConflict, "ITERATION_AUTOMATIC", "automatic mode manages iterations automatically")
	ErrIterationNotFound     = NewRuleError(http.StatusNotFound, "NOT_FOUND", "resource is not available")
	ErrIterationOverlap      = NewRuleError(http.StatusConflict, "ITERATION_OVERLAP", "iteration dates overlap another iteration")
	ErrIterationActiveExists = NewRuleError(http.StatusConflict, "ITERATION_ACTIVE_EXISTS", "the space already has an active iteration")
	ErrIterationsDisabled    = NewRuleError(http.StatusBadRequest, "ITERATIONS_DISABLED", "iterations are disabled for this space")
	ErrInvalidIteration      = NewRuleError(http.StatusBadRequest, "INVALID_ITERATION", "iteration is not available for this task")
	ErrInvalidTimezone       = NewRuleError(http.StatusBadRequest, "INVALID_TIMEZONE", "timezone is invalid")
	ErrIterationDate         = NewRuleError(http.StatusBadRequest, "INVALID_DATE", "iteration date is invalid")
	ErrIterationDateRange    = NewRuleError(http.StatusBadRequest, "INVALID_DATE_RANGE", "iteration end date must not be before its start date")
	ErrIterationState        = NewRuleError(http.StatusConflict, "ITERATION_STATE", "iteration state does not allow this operation")
	ErrIterationTarget       = NewRuleError(http.StatusBadRequest, "INVALID_ITERATION_TARGET", "target iteration is not available")
	ErrIterationLinkLimit    = NewRuleError(http.StatusConflict, "LIMIT_REACHED", "an iteration supports at most 20 links")
	ErrInvalidIterationURL   = NewRuleError(http.StatusBadRequest, "INVALID_LINK", "link must be an http(s) URL")
)

var validUpcomingCounts = map[int]bool{1: true, 2: true, 3: true, 4: true, 6: true, 8: true, 10: true, 12: true, 15: true}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

type Iteration struct {
	ID              string          `json:"id"`
	SpaceID         string          `json:"space_id"`
	Number          int             `json:"number"`
	Name            string          `json:"name"`
	DisplayName     string          `json:"display_name"`
	Description     string          `json:"description"`
	Status          string          `json:"status"`
	StartsAt        time.Time       `json:"starts_at"`
	EndsAt          time.Time       `json:"ends_at"`
	StartDate       string          `json:"start_date"`
	EndDate         string          `json:"end_date"`
	StartedAt       *time.Time      `json:"started_at"`
	CompletedAt     *time.Time      `json:"completed_at"`
	ScopeCount      *int            `json:"scope_count"`
	CompletedCount  *int            `json:"completed_count"`
	ScopePoints     *float64        `json:"scope_points"`
	CompletedPoints *float64        `json:"completed_points"`
	Live            IterationLive   `json:"live"`
	CapacityPercent *float64        `json:"capacity_percent"`
	Links           []IterationLink `json:"links"`
	Version         uint64          `json:"version"`
}

type IterationLive struct {
	ScopeCount      int     `json:"scope_count"`
	CompletedCount  int     `json:"completed_count"`
	ScopePoints     float64 `json:"scope_points"`
	CompletedPoints float64 `json:"completed_points"`
}

type IterationLink struct {
	ID          string    `json:"id"`
	IterationID string    `json:"iteration_id"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Position    int       `json:"position"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type IterationSettings struct {
	Enabled          bool       `json:"enabled"`
	Mode             string     `json:"mode"`
	DurationWeeks    int        `json:"duration_weeks"`
	CooldownWeeks    int        `json:"cooldown_weeks"`
	StartWeekday     int        `json:"start_weekday"`
	Timezone         string     `json:"timezone"`
	UpcomingCount    int        `json:"upcoming_count"`
	AutoAddStarted   bool       `json:"auto_add_started"`
	AutoAddCompleted bool       `json:"auto_add_completed"`
	NextStartDate    *string    `json:"next_start_date"`
	Current          *Iteration `json:"current"`
	Version          uint64     `json:"version"`
}

type IterationSettingsPatch struct {
	Version          uint64
	Enabled          *bool
	Mode             *string
	DurationWeeks    *int
	CooldownWeeks    *int
	StartWeekday     *int
	Timezone         *string
	UpcomingCount    *int
	AutoAddStarted   *bool
	AutoAddCompleted *bool
	NextStartDate    *string
}
type IterationCreateInput struct{ Name, Description, StartDate, EndDate string }
type IterationPatchInput struct {
	Version           uint64
	Name, Description *string
}
type IterationDatesInput struct {
	Version            uint64
	StartDate, EndDate *string
}
type IterationTaskBatchInput struct {
	TaskIDs []string
	Action  string
}
type IterationLinkInput struct {
	Title, URL string
	Position   int
}

type IterationListOptions struct {
	Status []string
	Limit  int
	Cursor string
}

func parseIterationDate(value string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, ErrIterationDate
	}
	return t, nil
}
func iterationTimes(startDate, endDate, timezone string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, time.Time{}, ErrInvalidTimezone
	}
	start, err := parseIterationDate(startDate)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := parseIterationDate(endDate)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, ErrIterationDateRange
	}
	return time.Date(start.Year(), start.Month(), start.Day(), 0, 1, 0, 0, loc), time.Date(end.Year(), end.Month(), end.Day()+1, 0, 1, 0, 0, loc), nil
}
func dateInLocation(t time.Time, timezone string) string {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return t.Format("2006-01-02")
	}
	return t.In(loc).Format("2006-01-02")
}
func iterationDisplayName(name string, number int) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return fmt.Sprintf("迭代 %d", number)
}
func nextWeekday(date time.Time, weekday int) time.Time {
	delta := (weekday - int(date.Weekday()) + 7) % 7
	return date.AddDate(0, 0, delta)
}

func scanIteration(row interface{ Scan(...any) error }, timezone string) (Iteration, error) {
	var item Iteration
	var version int64
	var scope, completed *int
	var points, donePoints *float64
	var err error
	if err = row.Scan(&item.ID, &item.SpaceID, &item.Number, &item.Name, &item.Description, &item.Status, &item.StartsAt, &item.EndsAt, &item.StartedAt, &item.CompletedAt, &scope, &completed, &points, &donePoints, &version); err != nil {
		return Iteration{}, err
	}
	item.ScopeCount, item.CompletedCount, item.ScopePoints, item.CompletedPoints = scope, completed, points, donePoints
	item.DisplayName = iterationDisplayName(item.Name, item.Number)
	item.StartDate = dateInLocation(item.StartsAt, timezone)
	// ends_at 是最后一天的次日 00:01，结束日 = 其本地日期的前一天。
	item.EndDate = dateInLocation(item.EndsAt.AddDate(0, 0, -1), timezone)
	item.Version, err = positiveVersion(version)
	if err != nil {
		return Iteration{}, err
	}
	item.Live = IterationLive{ScopeCount: valueInt(scope), CompletedCount: valueInt(completed), ScopePoints: valueFloat(points), CompletedPoints: valueFloat(donePoints)}
	return item, nil
}
func valueInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
func valueFloat(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// loadIterationConfig 读取空间的迭代配置，以及「下一个要创建的迭代」开始日（列值）；lock 时 FOR UPDATE。
func (s Service) loadIterationConfig(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, lock bool) (IterationSettings, *time.Time, error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return IterationSettings{}, nil, err
	}
	if !membership.ValidateUUID(spaceID) {
		return IterationSettings{}, nil, ErrSpaceNotFound
	}
	locking := ""
	if lock {
		locking = " FOR UPDATE"
	}
	var out IterationSettings
	var version int64
	var next *time.Time
	err := tx.QueryRow(ctx, `SELECT iterations_enabled, iteration_mode, iteration_duration_weeks, iteration_cooldown_weeks, iteration_start_weekday, iteration_timezone, iteration_upcoming_count, iteration_auto_add_started, iteration_auto_add_completed, iteration_next_start_date, version FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`+locking, spaceID).Scan(&out.Enabled, &out.Mode, &out.DurationWeeks, &out.CooldownWeeks, &out.StartWeekday, &out.Timezone, &out.UpcomingCount, &out.AutoAddStarted, &out.AutoAddCompleted, &next, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return IterationSettings{}, nil, ErrSpaceNotFound
	}
	if err != nil {
		return IterationSettings{}, nil, err
	}
	out.Version, err = positiveVersion(version)
	return out, next, err
}
func (s Service) loadIterationSettings(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, lock bool) (IterationSettings, error) {
	out, next, err := s.loadIterationConfig(ctx, tx, organizationID, spaceID, lock)
	if err != nil {
		return IterationSettings{}, err
	}
	// 展示的「下个迭代开始日」：最早的计划中迭代；没有则取待创建日。
	var first *time.Time
	if err := tx.QueryRow(ctx, `SELECT min(starts_at) FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'planned'`, spaceID).Scan(&first); err != nil {
		return IterationSettings{}, err
	}
	switch {
	case first != nil:
		value := dateInLocation(*first, out.Timezone)
		out.NextStartDate = &value
	case next != nil:
		value := next.Format("2006-01-02")
		out.NextStartDate = &value
	}
	out.Current, err = s.currentIteration(ctx, tx, spaceID, out.Timezone)
	return out, err
}
func (s Service) GetIterationSettings(ctx context.Context, tx pgx.Tx, organizationID, spaceID string) (IterationSettings, error) {
	return s.loadIterationSettings(ctx, tx, organizationID, spaceID, false)
}
func (s Service) currentIteration(ctx context.Context, tx pgx.Tx, spaceID, timezone string) (*Iteration, error) {
	row := tx.QueryRow(ctx, `SELECT `+iterationSelect()+` FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'active'`, spaceID)
	item, err := scanIteration(row, timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := []Iteration{item}
	if err := s.fillIterations(ctx, tx, spaceID, items); err != nil {
		return nil, err
	}
	return &items[0], nil
}

func mergeIterationSettings(p IterationSettingsPatch, cur IterationSettings) (IterationSettings, error) {
	out := cur
	if p.Enabled != nil {
		out.Enabled = *p.Enabled
	}
	if p.Mode != nil {
		out.Mode = strings.ToLower(strings.TrimSpace(*p.Mode))
	}
	for _, field := range []struct{ dst, src *int }{
		{&out.DurationWeeks, p.DurationWeeks}, {&out.CooldownWeeks, p.CooldownWeeks},
		{&out.StartWeekday, p.StartWeekday}, {&out.UpcomingCount, p.UpcomingCount},
	} {
		if field.src != nil {
			*field.dst = *field.src
		}
	}
	if p.Timezone != nil {
		out.Timezone = strings.TrimSpace(*p.Timezone)
	}
	if p.AutoAddStarted != nil {
		out.AutoAddStarted = *p.AutoAddStarted
	}
	if p.AutoAddCompleted != nil {
		out.AutoAddCompleted = *p.AutoAddCompleted
	}
	if _, err := time.LoadLocation(out.Timezone); err != nil || out.Timezone == "" {
		return out, ErrInvalidTimezone
	}
	if (out.Mode != "manual" && out.Mode != "automatic") || out.DurationWeeks < 1 || out.DurationWeeks > 8 ||
		out.CooldownWeeks < 0 || out.CooldownWeeks > 3 || out.StartWeekday < 0 || out.StartWeekday > 6 || !validUpcomingCounts[out.UpcomingCount] {
		return out, ErrInvalidIteration
	}
	return out, nil
}

func (s Service) UpdateIterationSettings(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, p IterationSettingsPatch) (IterationSettings, error) {
	if p.Version == 0 {
		return IterationSettings{}, ErrInvalidVersion
	}
	cur, nextColumn, err := s.loadIterationConfig(ctx, tx, organizationID, spaceID, true)
	if err != nil {
		return IterationSettings{}, err
	}
	if cur.Version != p.Version {
		return IterationSettings{}, ErrVersionConflict
	}
	if p.Enabled != nil && *p.Enabled && !cur.Enabled && p.Mode == nil {
		return IterationSettings{}, ErrInvalidIteration
	}
	merged, err := mergeIterationSettings(p, cur)
	if err != nil {
		return IterationSettings{}, err
	}
	var requested *time.Time
	if p.NextStartDate != nil {
		date, err := parseIterationDate(*p.NextStartDate)
		if err != nil {
			return IterationSettings{}, err
		}
		requested = &date
	}
	automatic := merged.Enabled && merged.Mode == "automatic"
	wasAutomatic := cur.Enabled && cur.Mode == "automatic"
	scheduleChanged := merged.DurationWeeks != cur.DurationWeeks || merged.CooldownWeeks != cur.CooldownWeeks ||
		merged.StartWeekday != cur.StartWeekday || merged.Timezone != cur.Timezone
	next := nextColumn
	if automatic && !wasAutomatic {
		// 启用或手动 → 自动：只在现有计划之后继续生成。
		if next = requested; next == nil {
			day, err := s.defaultNextStart(ctx, tx, spaceID, merged)
			if err != nil {
				return IterationSettings{}, err
			}
			next = &day
		}
	}
	result, err := tx.Exec(ctx, `UPDATE openfox.spaces SET iterations_enabled = $1, iteration_mode = $2, iteration_duration_weeks = $3, iteration_cooldown_weeks = $4, iteration_start_weekday = $5, iteration_timezone = $6, iteration_upcoming_count = $7, iteration_auto_add_started = $8, iteration_auto_add_completed = $9, iteration_next_start_date = $10::date, version = version + 1, updated_at = clock_timestamp() WHERE organization_id = openfox_private.current_organization_id() AND id = $11::uuid AND version = $12 AND deleted_at IS NULL`, merged.Enabled, merged.Mode, merged.DurationWeeks, merged.CooldownWeeks, merged.StartWeekday, merged.Timezone, merged.UpcomingCount, merged.AutoAddStarted, merged.AutoAddCompleted, next, spaceID, int64(p.Version))
	if err != nil {
		return IterationSettings{}, err
	}
	if result.RowsAffected() != 1 {
		return IterationSettings{}, ErrVersionConflict
	}
	if automatic {
		if wasAutomatic && (requested != nil || scheduleChanged) {
			if err := s.redateForSettings(ctx, tx, spaceID, cur, merged, requested, nextColumn); err != nil {
				return IterationSettings{}, err
			}
			if next, err = s.nextStartColumn(ctx, tx, spaceID); err != nil {
				return IterationSettings{}, err
			}
		}
		// 补齐 upcoming_count 个，并在今天已落入第一个迭代区间时立即开始它。
		if err := s.scheduleSpace(ctx, tx, spaceID, merged, *next, s.now()); err != nil {
			return IterationSettings{}, err
		}
	}
	return s.loadIterationSettings(ctx, tx, organizationID, spaceID, false)
}

// redateForSettings 在时长 / 冷却 / 起始日 / 时区 / 下个开始日变化后重排计划中的迭代，活跃迭代不变。
func (s Service) redateForSettings(ctx context.Context, tx pgx.Tx, spaceID string, cur, merged IterationSettings, requested, nextColumn *time.Time) error {
	anchor := requested
	if anchor == nil {
		anchor = nextColumn
		var first *time.Time
		if err := tx.QueryRow(ctx, `SELECT min(starts_at) FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'planned'`, spaceID).Scan(&first); err != nil {
			return err
		}
		if first != nil {
			loc, _ := time.LoadLocation(cur.Timezone)
			day := civilDate(*first, loc)
			anchor = &day
		}
		if merged.StartWeekday != cur.StartWeekday {
			day := nextWeekday(*anchor, merged.StartWeekday)
			anchor = &day
		}
	}
	starts, _, err := iterationTimes(anchor.Format("2006-01-02"), anchor.Format("2006-01-02"), merged.Timezone)
	if err != nil {
		return err
	}
	var overlap bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'active' AND ends_at > $2)`, spaceID, starts).Scan(&overlap); err != nil {
		return err
	}
	if overlap {
		return ErrIterationOverlap
	}
	return s.redatePlanned(ctx, tx, spaceID, merged, *anchor, time.Time{}, "")
}
func (s Service) nextStartColumn(ctx context.Context, tx pgx.Tx, spaceID string) (*time.Time, error) {
	var next *time.Time
	err := tx.QueryRow(ctx, `SELECT iteration_next_start_date FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, spaceID).Scan(&next)
	return next, err
}

func iterationSelect() string {
	return `id::text, space_id::text, number, name, description, status, starts_at, ends_at, started_at, completed_at, scope_count, completed_count, scope_points::float8, completed_points::float8, version`
}

func (s Service) iterationTimezone(ctx context.Context, tx pgx.Tx, spaceID string) (string, error) {
	var timezone string
	err := tx.QueryRow(ctx, `SELECT iteration_timezone FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, spaceID).Scan(&timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrSpaceNotFound
	}
	return timezone, err
}
func (s Service) ListIterations(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, options IterationListOptions) (Page[Iteration], error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return Page[Iteration]{}, err
	}
	if options.Limit < 1 || options.Limit > MaxPageSize {
		return Page[Iteration]{}, ErrInvalidPageSize
	}
	if !membership.ValidateUUID(spaceID) {
		return Page[Iteration]{}, ErrSpaceNotFound
	}
	timezone, err := s.iterationTimezone(ctx, tx, spaceID)
	if err != nil {
		return Page[Iteration]{}, err
	}
	where := []string{"organization_id = openfox_private.current_organization_id()", "space_id = $1::uuid"}
	args := []any{spaceID}
	if len(options.Status) > 0 {
		statuses := make([]string, 0, len(options.Status))
		for _, status := range options.Status {
			if status == "current" {
				status = "active"
			}
			if status != "planned" && status != "active" && status != "completed" && status != "canceled" {
				return Page[Iteration]{}, ErrInvalidIteration
			}
			statuses = append(statuses, status)
		}
		args = append(args, statuses)
		where = append(where, fmt.Sprintf("status = ANY($%d::text[])", len(args)))
	}
	if options.Cursor != "" {
		startsAt, id, err := parseIterationCursor(options.Cursor)
		if err != nil {
			return Page[Iteration]{}, err
		}
		args = append(args, startsAt, id)
		where = append(where, fmt.Sprintf("(starts_at, id) > ($%d, $%d::uuid)", len(args)-1, len(args)))
	}
	args = append(args, options.Limit+1)
	rows, err := tx.Query(ctx, `SELECT `+iterationSelect()+` FROM openfox.iterations WHERE `+strings.Join(where, " AND ")+fmt.Sprintf(" ORDER BY starts_at, id LIMIT $%d", len(args)), args...)
	if err != nil {
		return Page[Iteration]{}, err
	}
	defer rows.Close()
	items := make([]Iteration, 0, options.Limit+1)
	for rows.Next() {
		item, err := scanIteration(rows, timezone)
		if err != nil {
			return Page[Iteration]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[Iteration]{}, err
	}
	rows.Close()
	page := Page[Iteration]{}
	if len(items) > options.Limit {
		items = items[:options.Limit]
		page.NextCursor = iterationCursor(items[len(items)-1])
	}
	if err := s.fillIterations(ctx, tx, spaceID, items); err != nil {
		return Page[Iteration]{}, err
	}
	page.Items = items
	return page, nil
}

// fillIterations 补齐计划中 / 活跃迭代的实时计数（一次聚合）和容量百分比（相对最近三个已完成迭代的平均完成点数）。
func (s Service) fillIterations(ctx context.Context, tx pgx.Tx, spaceID string, items []Iteration) error {
	var ids []string
	for _, item := range items {
		if item.Status == "planned" || item.Status == "active" {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	live, err := iterationAggregates(ctx, tx, ids)
	if err != nil {
		return err
	}
	var average *float64
	if err := tx.QueryRow(ctx, `SELECT avg(completed_points)::float8 FROM (SELECT completed_points FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'completed' ORDER BY completed_at DESC LIMIT 3) h`, spaceID).Scan(&average); err != nil {
		return err
	}
	for i := range items {
		if items[i].Status != "planned" && items[i].Status != "active" {
			continue
		}
		items[i].Live = live[items[i].ID]
		if average != nil && *average > 0 {
			percent := math.Round(items[i].Live.ScopePoints / *average * 1000) / 10
			items[i].CapacityPercent = &percent
		}
	}
	return nil
}
func (s Service) GetIteration(ctx context.Context, tx pgx.Tx, organizationID, spaceID, iterationID string) (Iteration, error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return Iteration{}, err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(iterationID) {
		return Iteration{}, ErrIterationNotFound
	}
	timezone, err := s.iterationTimezone(ctx, tx, spaceID)
	if errors.Is(err, ErrSpaceNotFound) {
		return Iteration{}, ErrIterationNotFound
	}
	if err != nil {
		return Iteration{}, err
	}
	item, err := scanIteration(tx.QueryRow(ctx, `SELECT `+iterationSelect()+` FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`, spaceID, iterationID), timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return Iteration{}, ErrIterationNotFound
	}
	if err != nil {
		return Iteration{}, err
	}
	items := []Iteration{item}
	if err := s.fillIterations(ctx, tx, spaceID, items); err != nil {
		return Iteration{}, err
	}
	item = items[0]
	rows, err := tx.Query(ctx, `SELECT id::text, iteration_id::text, title, url, position, created_by::text, created_at FROM openfox.iteration_links WHERE organization_id = openfox_private.current_organization_id() AND iteration_id = $1::uuid ORDER BY position, id`, iterationID)
	if err != nil {
		return Iteration{}, err
	}
	defer rows.Close()
	item.Links = []IterationLink{}
	for rows.Next() {
		var link IterationLink
		if err := rows.Scan(&link.ID, &link.IterationID, &link.Title, &link.URL, &link.Position, &link.CreatedBy, &link.CreatedAt); err != nil {
			return Iteration{}, err
		}
		item.Links = append(item.Links, link)
	}
	return item, rows.Err()
}

func (s Service) CreateIteration(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input IterationCreateInput) (Iteration, error) {
	settings, err := s.loadIterationSettings(ctx, tx, organizationID, spaceID, true)
	if err != nil {
		return Iteration{}, err
	}
	if !settings.Enabled {
		return Iteration{}, ErrIterationsDisabled
	}
	if settings.Mode == "automatic" {
		return Iteration{}, ErrIterationAutomatic
	}
	starts, ends, err := iterationTimes(input.StartDate, input.EndDate, settings.Timezone)
	if err != nil {
		return Iteration{}, err
	}
	var overlap bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status <> 'canceled' AND starts_at < $3 AND ends_at > $2)`, spaceID, starts, ends).Scan(&overlap); err != nil {
		return Iteration{}, err
	}
	if overlap {
		return Iteration{}, ErrIterationOverlap
	}
	var number int
	var id string
	if err := tx.QueryRow(ctx, `UPDATE openfox.spaces SET next_iteration_number = next_iteration_number + 1 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid RETURNING gen_random_uuid()::text, next_iteration_number - 1`, spaceID).Scan(&id, &number); err != nil {
		return Iteration{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO openfox.iterations (organization_id, space_id, id, number, name, description, status, starts_at, ends_at, created_by) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5, 'planned', $6, $7, $8::uuid)`, spaceID, id, number, strings.TrimSpace(input.Name), input.Description, starts, ends, actor.UserID); err != nil {
		return Iteration{}, err
	}
	return s.GetIteration(ctx, tx, organizationID, spaceID, id)
}

func (s Service) UpdateIteration(ctx context.Context, tx pgx.Tx, organizationID, spaceID, iterationID string, input IterationPatchInput) (Iteration, error) {
	if input.Version == 0 {
		return Iteration{}, ErrInvalidVersion
	}
	if input.Name != nil && len([]rune(*input.Name)) > 80 || input.Description != nil && len([]rune(*input.Description)) > 4000 {
		return Iteration{}, ErrInvalidIteration
	}
	args := []any{spaceID, iterationID, int64(input.Version)}
	sets := []string{}
	if input.Name != nil {
		sets = append(sets, fmt.Sprintf("name = $%d", len(args)+1))
		args = append(args, strings.TrimSpace(*input.Name))
	}
	if input.Description != nil {
		sets = append(sets, fmt.Sprintf("description = $%d", len(args)+1))
		args = append(args, *input.Description)
	}
	if len(sets) == 0 {
		return s.GetIteration(ctx, tx, organizationID, spaceID, iterationID)
	}
	sets = append(sets, "version = version + 1", "updated_at = clock_timestamp()")
	result, err := tx.Exec(ctx, `UPDATE openfox.iterations SET `+strings.Join(sets, ", ")+` WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid AND version = $3`, args...)
	if err != nil {
		return Iteration{}, err
	}
	if result.RowsAffected() != 1 {
		return Iteration{}, ErrVersionConflict
	}
	return s.GetIteration(ctx, tx, organizationID, spaceID, iterationID)
}
func (s Service) UpdateIterationDates(ctx context.Context, tx pgx.Tx, organizationID, spaceID, iterationID string, input IterationDatesInput) (Iteration, error) {
	if input.Version == 0 {
		return Iteration{}, ErrInvalidVersion
	}
	// 锁空间行，与调度器、其他日期修改串行。
	settings, _, err := s.loadIterationConfig(ctx, tx, organizationID, spaceID, true)
	if err != nil {
		return Iteration{}, err
	}
	current, err := s.GetIteration(ctx, tx, organizationID, spaceID, iterationID)
	if err != nil {
		return Iteration{}, err
	}
	if current.Version != input.Version {
		return Iteration{}, ErrVersionConflict
	}
	if current.Status != "planned" && current.Status != "active" {
		return Iteration{}, ErrIterationState
	}
	start, end := current.StartDate, current.EndDate
	if input.StartDate != nil {
		start = strings.TrimSpace(*input.StartDate)
	}
	if input.EndDate != nil {
		end = strings.TrimSpace(*input.EndDate)
	}
	starts, ends, err := iterationTimes(start, end, settings.Timezone)
	if err != nil {
		return Iteration{}, err
	}
	// 已过去的日期不能改：新的开始日（有变化时）和结束日都不能早于今天。
	if today := dateInLocation(s.now(), settings.Timezone); end < today || (start != current.StartDate && start < today) {
		return Iteration{}, ErrIterationDate
	}
	automatic := settings.Enabled && settings.Mode == "automatic"
	if !automatic {
		var overlap bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id <> $2::uuid AND status <> 'canceled' AND starts_at < $4 AND ends_at > $3)`, spaceID, iterationID, starts, ends).Scan(&overlap); err != nil {
			return Iteration{}, err
		}
		if overlap {
			return Iteration{}, ErrIterationOverlap
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.iterations SET starts_at = $1, ends_at = $2, version = version + 1, updated_at = clock_timestamp() WHERE organization_id = openfox_private.current_organization_id() AND space_id = $3::uuid AND id = $4::uuid`, starts, ends, spaceID, iterationID); err != nil {
		return Iteration{}, err
	}
	if automatic {
		// 自动模式下改日期会顺移其后的计划中迭代。
		endDate, _ := parseIterationDate(end)
		if err := s.redatePlanned(ctx, tx, spaceID, settings, endDate.AddDate(0, 0, settings.CooldownWeeks*7+1), current.StartsAt, iterationID); err != nil {
			return Iteration{}, err
		}
	}
	return s.GetIteration(ctx, tx, organizationID, spaceID, iterationID)
}

func (s Service) StartIteration(ctx context.Context, tx pgx.Tx, organizationID, spaceID, iterationID string, version uint64) (Iteration, error) {
	settings, err := s.GetIterationSettings(ctx, tx, organizationID, spaceID)
	if err != nil {
		return Iteration{}, err
	}
	if settings.Mode == "automatic" {
		return Iteration{}, ErrIterationAutomatic
	}
	if version == 0 {
		return Iteration{}, ErrInvalidVersion
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'active')`, spaceID).Scan(&active); err != nil {
		return Iteration{}, err
	}
	if active {
		return Iteration{}, ErrIterationActiveExists
	}
	result, err := tx.Exec(ctx, `UPDATE openfox.iterations SET status = 'active', started_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp() WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid AND status = 'planned' AND version = $3`, spaceID, iterationID, int64(version))
	if err != nil {
		return Iteration{}, err
	}
	if result.RowsAffected() != 1 {
		return Iteration{}, ErrVersionConflict
	}
	return s.GetIteration(ctx, tx, organizationID, spaceID, iterationID)
}
func (s Service) CompleteIteration(ctx context.Context, tx pgx.Tx, organizationID, spaceID, iterationID string, actor Actor, version uint64, target string) (Iteration, error) {
	settings, err := s.GetIterationSettings(ctx, tx, organizationID, spaceID)
	if err != nil {
		return Iteration{}, err
	}
	if settings.Mode == "automatic" {
		return Iteration{}, ErrIterationAutomatic
	}
	if version == 0 {
		return Iteration{}, ErrInvalidVersion
	}
	item, err := s.GetIteration(ctx, tx, organizationID, spaceID, iterationID)
	if err != nil {
		return Iteration{}, err
	}
	if item.Status != "active" {
		return Iteration{}, ErrIterationState
	}
	// 未完成任务的去向：backlog（默认）/ next（最早的计划中迭代，没有则 backlog）/ 指定的计划中迭代。
	targetID := ""
	switch target {
	case "", "backlog":
	case "next":
		if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND status = 'planned' ORDER BY starts_at, id LIMIT 1`, spaceID).Scan(&targetID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Iteration{}, err
		}
	default:
		if !membership.ValidateUUID(target) {
			return Iteration{}, ErrIterationTarget
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid AND status = 'planned'`, spaceID, target).Scan(&targetID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Iteration{}, ErrIterationTarget
			}
			return Iteration{}, err
		}
	}
	if err := s.completeIteration(ctx, tx, spaceID, iterationID, version, targetID, s.now(), actor.UserID); err != nil {
		return Iteration{}, err
	}
	return s.GetIteration(ctx, tx, organizationID, spaceID, iterationID)
}
func (s Service) DeleteIteration(ctx context.Context, tx pgx.Tx, organizationID, spaceID, iterationID string, version uint64) error {
	if version == 0 {
		return ErrInvalidVersion
	}
	item, err := s.GetIteration(ctx, tx, organizationID, spaceID, iterationID)
	if err != nil {
		return err
	}
	if item.Status != "planned" && item.Status != "canceled" {
		return ErrIterationState
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.tasks SET iteration_id = NULL, version = version + 1, updated_at = clock_timestamp() WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND iteration_id = $2::uuid`, spaceID, iterationID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `DELETE FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid AND version = $3`, spaceID, iterationID, int64(version))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return nil
}

func (s Service) UpdateIterationTasks(ctx context.Context, tx pgx.Tx, organizationID, spaceID, iterationID string, input IterationTaskBatchInput) (int, error) {
	if len(input.TaskIDs) == 0 || len(input.TaskIDs) > 200 || (input.Action != "add" && input.Action != "remove") {
		return 0, ErrInvalidIteration
	}
	if !membership.ValidateUUID(iterationID) {
		return 0, ErrIterationNotFound
	}
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM openfox.iterations WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`, spaceID, iterationID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrIterationNotFound
	}
	if err != nil {
		return 0, err
	}
	if status != "planned" && status != "active" {
		return 0, ErrInvalidIteration
	}
	if input.Action == "add" {
		result, err := tx.Exec(ctx, `UPDATE openfox.tasks SET iteration_id = $1::uuid, version = version + 1, updated_at = clock_timestamp() WHERE organization_id = openfox_private.current_organization_id() AND space_id = $2::uuid AND id = ANY($3::uuid[]) AND deleted_at IS NULL`, iterationID, spaceID, input.TaskIDs)
		return int(result.RowsAffected()), err
	}
	result, err := tx.Exec(ctx, `UPDATE openfox.tasks SET iteration_id = NULL, version = version + 1, updated_at = clock_timestamp() WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = ANY($2::uuid[]) AND iteration_id = $3::uuid AND deleted_at IS NULL`, spaceID, input.TaskIDs, iterationID)
	return int(result.RowsAffected()), err
}
func (s Service) CreateIterationLink(ctx context.Context, tx pgx.Tx, organizationID, spaceID, iterationID string, actor Actor, input IterationLinkInput) (IterationLink, error) {
	if parsed, err := url.Parse(strings.TrimSpace(input.URL)); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return IterationLink{}, ErrInvalidIterationURL
	}
	if len([]rune(input.Title)) > 200 || input.Position < 0 || input.Position >= 20 {
		return IterationLink{}, ErrInvalidIteration
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.iteration_links WHERE organization_id = openfox_private.current_organization_id() AND iteration_id = $1::uuid`, iterationID).Scan(&count); err != nil {
		return IterationLink{}, err
	}
	if count >= 20 {
		return IterationLink{}, ErrIterationLinkLimit
	}
	id, err := membership.NewID()
	if err != nil {
		return IterationLink{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO openfox.iteration_links (organization_id, iteration_id, id, title, url, position, created_by) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5, $6::uuid)`, iterationID, id, strings.TrimSpace(input.Title), strings.TrimSpace(input.URL), input.Position, actor.UserID); err != nil {
		return IterationLink{}, err
	}
	var item IterationLink
	err = tx.QueryRow(ctx, `SELECT id::text, iteration_id::text, title, url, position, created_by::text, created_at FROM openfox.iteration_links WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id).Scan(&item.ID, &item.IterationID, &item.Title, &item.URL, &item.Position, &item.CreatedBy, &item.CreatedAt)
	return item, err
}
func (s Service) DeleteIterationLink(ctx context.Context, tx pgx.Tx, organizationID, spaceID, iterationID, linkID string) error {
	result, err := tx.Exec(ctx, `DELETE FROM openfox.iteration_links WHERE organization_id = openfox_private.current_organization_id() AND iteration_id = $1::uuid AND id = $2::uuid`, iterationID, linkID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrIterationNotFound
	}
	return nil
}

func iterationCursor(item Iteration) string {
	payload, _ := json.Marshal([]string{item.StartsAt.UTC().Format(time.RFC3339Nano), item.ID})
	return base64.RawURLEncoding.EncodeToString(payload)
}
func parseIterationCursor(raw string) (time.Time, string, error) {
	bytes, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	var values []string
	if json.Unmarshal(bytes, &values) != nil || len(values) != 2 {
		return time.Time{}, "", ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, values[0])
	if err != nil || !membership.ValidateUUID(values[1]) {
		return time.Time{}, "", ErrInvalidCursor
	}
	return t, values[1], nil
}
