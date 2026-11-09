package identity

// 通知渠道设置。每人每个渠道一行；邮件没有行时默认关闭。

import (
	"context"
	"errors"
	"slices"
)

var ErrInvalidNotificationSettings = errors.New("identity: notification settings are invalid")
var ErrEmailNotificationsUnavailable = errors.New("identity: email notifications are unavailable")

// NotificationChannels 的顺序即返回顺序。
var NotificationChannels = []string{"desktop", "browser", "feishu", "email", "wecom"}

// NotificationEvents 是可关闭的事件类型，顺序与契约一致。
var NotificationEvents = []string{"assign", "status", "comment", "mention", "agent", "automation"}

// NotificationChannel 是一个渠道的设置。DisabledEvents 永不为 nil；Customized 为 false 表示默认设置。
type NotificationChannel struct {
	DirectMessages *bool
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
	DeleteNotificationChannel(ctx context.Context, userID, channel string) error
}

func defaultNotificationChannel(channel string) NotificationChannel {
	direct := channel != "feishu" && channel != "email" && channel != "wecom"
	return NotificationChannel{Channel: channel, Enabled: channel != "email" && channel != "wecom", Available: true, DisabledEvents: []string{}, DirectMessages: &direct}
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
func (s Service) ResetNotificationChannel(ctx context.Context, userID, channel string) (NotificationChannel, error) {
	store, ok := s.Store.(NotificationChannelStore)
	if !ok {
		return NotificationChannel{}, ErrAccountUnsupported
	}
	if !validNotificationChannel(channel) {
		return NotificationChannel{}, ErrInvalidNotificationSettings
	}
	if err := store.DeleteNotificationChannel(ctx, userID, channel); err != nil {
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
