package identity

// 通知渠道设置。每人每个渠道一行；邮件没有行时默认关闭。

import (
	"context"
	"errors"
	"slices"
)

var ErrInvalidNotificationSettings = errors.New("identity: notification settings are invalid")
var ErrNotificationRevisionConflict = errors.New("identity: notification revision conflict")

var ErrEmailNotificationsUnavailable = errors.New("identity: email notifications are unavailable")

// NotificationChannels 的顺序即返回顺序。
var NotificationChannels = []string{"desktop", "browser", "feishu", "email", "wecom", "lark", "dingtalk", "slack"}

// NotificationEvents 是可关闭的事件类型，顺序与契约一致。
var NotificationEvents = []string{"assign", "status", "comment", "mention", "agent", "automation"}

// NotificationChannel 是一个渠道的设置。DisabledEvents 永不为 nil；Customized 为 false 表示默认设置。
type NotificationChannel struct {
	DirectMessages *bool
	EventOverrides map[string]bool
	MutedGroups    []string
	Revision       int64
	Channel        string
	Enabled        bool
	Available      bool
	DisabledEvents []string
	Customized     bool
}

// NotificationChannelStore 是可选的通知设置能力。
type NotificationChannelStore interface {
	// LoadNotificationChannels 只返回已保存的行，键为渠道名。
	LoadNotificationChannels(ctx context.Context, userID string) (map[string]NotificationChannel, error)
	// SaveNotificationChannel 按主键 upsert。
	SaveNotificationChannel(ctx context.Context, userID string, channel *NotificationChannel) error
	// DeleteNotificationChannel 删除该行，不存在时不报错。
	DeleteNotificationChannel(ctx context.Context, userID, channel string, revision *int64) error
	PatchNotificationChannel(ctx context.Context, userID, channel string, patch NotificationChannelPatch) (NotificationChannel, error)
}

func defaultNotificationChannel(channel string) NotificationChannel {
	direct := channel == "desktop" || channel == "browser"
	return NotificationChannel{Channel: channel, Enabled: channel != "email" && channel != "wecom" && channel != "slack", Available: true, DisabledEvents: []string{}, DirectMessages: &direct, EventOverrides: map[string]bool{}, MutedGroups: []string{}}
}

// ListNotificationChannels 返回所有渠道，没有保存过的渠道返回默认值。
func (s Service) ListNotificationChannels(ctx context.Context, userID string) ([]NotificationChannel, error) {
	store, ok := s.Store.(NotificationChannelStore)
	if !ok {
		return nil, ErrAccountUnsupported
	}
	saved, err := store.LoadNotificationChannels(ctx, userID)
	if err != nil {
		return nil, err
	}
	items := make([]NotificationChannel, 0, len(NotificationChannels))
	for _, channel := range NotificationChannels {
		item, ok := saved[channel]
		if !ok {
			item = defaultNotificationChannel(channel)
		}
		item.Available = channel != "email" || s.EmailSender != nil
		items = append(items, item)
	}
	return items, nil
}

// SaveNotificationChannel 整体替换一个渠道的设置。事件去重、排序后保存。
func (s Service) SaveNotificationChannel(ctx context.Context, userID, channel string, enabled bool, disabledEvents []string, direct *bool) (NotificationChannel, error) {
	store, ok := s.Store.(NotificationChannelStore)
	if !ok {
		return NotificationChannel{}, ErrAccountUnsupported
	}
	if !validNotificationChannel(channel) {
		return NotificationChannel{}, ErrInvalidNotificationSettings
	}
	if channel == "email" && enabled && s.EmailSender == nil {
		return NotificationChannel{}, ErrEmailNotificationsUnavailable
	}
	events, err := normalizeDisabledEvents(disabledEvents)
	if err != nil {
		return NotificationChannel{}, err
	}
	next := NotificationChannel{DirectMessages: direct, Channel: channel, Enabled: enabled, Available: channel != "email" || s.EmailSender != nil, DisabledEvents: events, Customized: true}
	if err := store.SaveNotificationChannel(ctx, userID, &next); err != nil {
		return NotificationChannel{}, err
	}
	return next, nil
}

// ResetNotificationChannel 删除保存的设置，返回默认值。
func (s Service) ResetNotificationChannel(ctx context.Context, userID, channel string, versions ...*int64) (NotificationChannel, error) {
	store, ok := s.Store.(NotificationChannelStore)
	if !ok {
		return NotificationChannel{}, ErrAccountUnsupported
	}
	if !validNotificationChannel(channel) {
		return NotificationChannel{}, ErrInvalidNotificationSettings
	}
	var revision *int64
	if len(versions) > 0 {
		revision = versions[0]
	}
	if revision != nil && *revision < 0 {
		return NotificationChannel{}, ErrInvalidNotificationSettings
	}
	if err := store.DeleteNotificationChannel(ctx, userID, channel, revision); err != nil {
		return NotificationChannel{}, err
	}
	item := defaultNotificationChannel(channel)
	item.Available = channel != "email" || s.EmailSender != nil
	return item, nil
}

func validNotificationChannel(channel string) bool {
	return slices.Contains(NotificationChannels, channel)
}

// normalizeDisabledEvents 校验事件类型，去重并按字母序排序；返回值永不为 nil。
func normalizeDisabledEvents(events []string) ([]string, error) {
	result := make([]string, 0, len(events))
	for _, event := range events {
		if !slices.Contains(NotificationEvents, event) {
			return nil, ErrInvalidNotificationSettings
		}
		if !slices.Contains(result, event) {
			result = append(result, event)
		}
	}
	slices.Sort(result)
	return result, nil
}

// PATCH changes only the supplied values; the revision protects concurrent clients.
type NotificationChannelPatch struct {
	Revision       *int64           `json:"revision"`
	Enabled        *bool            `json:"enabled"`
	DirectMessages *bool            `json:"direct_messages"`
	EventOverrides map[string]*bool `json:"event_overrides"`
	MutedGroups    *[]string        `json:"muted_groups"`
}

func (s Service) PatchNotificationChannel(ctx context.Context, userID, channel string, patch NotificationChannelPatch) (NotificationChannel, error) {
	store, ok := s.Store.(NotificationChannelStore)
	if !ok {
		return NotificationChannel{}, ErrAccountUnsupported
	}
	if !validNotificationChannel(channel) || patch.Revision == nil || *patch.Revision < 0 || len(patch.EventOverrides) > 42 || (patch.MutedGroups != nil && len(*patch.MutedGroups) > 7) {
		return NotificationChannel{}, ErrInvalidNotificationSettings
	}
	for _, value := range patch.EventOverrides {
		if value == nil {
			return NotificationChannel{}, ErrInvalidNotificationSettings
		}
	}
	if patch.MutedGroups != nil {
		seen := map[string]bool{}
		for _, group := range *patch.MutedGroups {
			if seen[group] {
				return NotificationChannel{}, ErrInvalidNotificationSettings
			}
			seen[group] = true
		}
	}
	if patch.Enabled != nil && *patch.Enabled && channel == "email" && s.EmailSender == nil {
		return NotificationChannel{}, ErrEmailNotificationsUnavailable
	}
	next, err := store.PatchNotificationChannel(ctx, userID, channel, patch)
	next.Available = channel != "email" || s.EmailSender != nil
	return next, err
}
