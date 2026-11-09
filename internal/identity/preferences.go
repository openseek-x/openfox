package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidPreferences         = errors.New("identity: preferences are invalid")
	ErrPreferencesVersionConflict = errors.New("identity: preferences version conflict")
	ErrShortcutConflict           = errors.New("identity: command shortcuts conflict")
)

const maxShortcutLength = 40

// defaultCommandShortcuts 是可自定义的三个命令及其默认按键。自定义值与默认相同时不入库。
var defaultCommandShortcuts = map[string]string{
	"command_menu":  "mod+k",
	"move_to_space": "mod+shift+m",
	"find_in_view":  "mod+f",
}

// Preferences 是账号偏好。CommandShortcuts 只含自定义过的命令，缺省即默认按键。
// Version 为 0 表示还没有保存过（返回的是默认值）。
type Preferences struct {
	Theme                     string
	Density                   string
	Locale                    *string
	OutlineColor              int
	OpenSessionOnAssign       bool
	OpenSessionOnQuickCommand bool
	SendShortcut              string
	CommandShortcuts          map[string]string
	Version                   int64
}

// PreferencesPatch 只改出现的字段（nil = 不改）。CommandShortcuts 非 nil 时整体替换。
type PreferencesPatch struct {
	Version                   *int64
	Theme                     *string
	Density                   *string
	Locale                    *string
	OutlineColor              *int
	OpenSessionOnAssign       *bool
	OpenSessionOnQuickCommand *bool
	SendShortcut              *string
	CommandShortcuts          map[string]string
}

// PreferenceStore 是可选的偏好能力。
type PreferenceStore interface {
	LoadPreferences(context.Context, string) (Preferences, error)
	// SavePreferences 在 expectedVersion 仍是当前版本时写入 next，版本号加一并返回；否则 ErrPreferencesVersionConflict。
	SavePreferences(context.Context, string, int64, Preferences) (Preferences, error)
}

func defaultPreferences() Preferences {
	return Preferences{
		Theme:                     "system",
		Density:                   "comfortable",
		OpenSessionOnAssign:       true,
		OpenSessionOnQuickCommand: true,
		SendShortcut:              "enter",
		CommandShortcuts:          map[string]string{},
	}
}

// Preferences 返回账号偏好；从未保存过时返回默认值（Version = 0）。
func (s Service) Preferences(ctx context.Context, userID string) (Preferences, error) {
	store, ok := s.Store.(PreferenceStore)
	if !ok {
		return Preferences{}, ErrAccountUnsupported
	}
	return store.LoadPreferences(ctx, userID)
}

// UpdatePreferences 合并补丁后保存。版本不一致返回 ErrPreferencesVersionConflict。
func (s Service) UpdatePreferences(ctx context.Context, userID string, patch PreferencesPatch) (Preferences, error) {
	store, ok := s.Store.(PreferenceStore)
	if !ok {
		return Preferences{}, ErrAccountUnsupported
	}
	if patch.Version == nil || *patch.Version < 0 {
		return Preferences{}, ErrInvalidPreferences
	}
	current, err := store.LoadPreferences(ctx, userID)
	if err != nil {
		return Preferences{}, err
	}
	if current.Version != *patch.Version {
		return Preferences{}, ErrPreferencesVersionConflict
	}
	next, err := applyPreferencesPatch(current, patch)
	if err != nil {
		return Preferences{}, err
	}
	return store.SavePreferences(ctx, userID, current.Version, next)
}

func applyPreferencesPatch(current Preferences, patch PreferencesPatch) (Preferences, error) {
	next := current
	if patch.Density != nil {
		if !slices.Contains([]string{"comfortable", "compact"}, *patch.Density) {
			return Preferences{}, ErrInvalidPreferences
		}
		next.Density = *patch.Density
	}
	if patch.Theme != nil {
		if !slices.Contains([]string{"system", "light", "dark", "magic-blue", "classic-dark"}, *patch.Theme) {
			return Preferences{}, ErrInvalidPreferences
		}
		next.Theme = *patch.Theme
	}
	if patch.Locale != nil {
		if !slices.Contains([]string{"zh-CN", "en-US"}, *patch.Locale) {
			return Preferences{}, ErrInvalidPreferences
		}
		locale := *patch.Locale
		next.Locale = &locale
	}
	if patch.OutlineColor != nil {
		if *patch.OutlineColor < 0 || *patch.OutlineColor > 12 {
			return Preferences{}, ErrInvalidPreferences
		}
		next.OutlineColor = *patch.OutlineColor
	}
	if patch.OpenSessionOnAssign != nil {
		next.OpenSessionOnAssign = *patch.OpenSessionOnAssign
	}
	if patch.OpenSessionOnQuickCommand != nil {
		next.OpenSessionOnQuickCommand = *patch.OpenSessionOnQuickCommand
	}
	if patch.SendShortcut != nil {
		if !slices.Contains([]string{"enter", "shift_enter"}, *patch.SendShortcut) {
			return Preferences{}, ErrInvalidPreferences
		}
		next.SendShortcut = *patch.SendShortcut
	}
	if patch.CommandShortcuts != nil {
		shortcuts, err := normalizeCommandShortcuts(patch.CommandShortcuts)
		if err != nil {
			return Preferences{}, err
		}
		next.CommandShortcuts = shortcuts
	}
	if err := checkShortcutConflicts(next.CommandShortcuts); err != nil {
		return Preferences{}, err
	}
	return next, nil
}

// normalizeCommandShortcuts 校验命令名与按键，规范化后丢弃与默认相同的值。
func normalizeCommandShortcuts(input map[string]string) (map[string]string, error) {
	result := map[string]string{}
	for command, value := range input {
		defaultValue, known := defaultCommandShortcuts[command]
		if !known {
			return nil, fmt.Errorf("%w: unknown command %q", ErrInvalidPreferences, command)
		}
		canonical, err := normalizeShortcut(value)
		if err != nil {
			return nil, err
		}
		if canonical != defaultValue {
			result[command] = canonical
		}
	}
	return result, nil
}

// checkShortcutConflicts 检查三个命令最终生效的按键（自定义或默认）是否重复。
func checkShortcutConflicts(custom map[string]string) error {
	taken := map[string]bool{}
	for command, defaultValue := range defaultCommandShortcuts {
		value, ok := custom[command]
		if !ok {
			value = defaultValue
		}
		if taken[value] {
			return ErrShortcutConflict
		}
		taken[value] = true
	}
	return nil
}

// normalizeShortcut 把输入规范化为 mod[+shift][+alt]+<键>（小写）。修饰键顺序任意，必须含 mod，
// 键只能是一个字母、数字或 / , . ; [ ]。
func normalizeShortcut(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || utf8.RuneCountInString(value) > maxShortcutLength {
		return "", ErrInvalidPreferences
	}
	modifiers := map[string]bool{}
	key := ""
	for _, part := range strings.Split(value, "+") {
		part = strings.TrimSpace(part)
		switch {
		case part == "mod" || part == "shift" || part == "alt":
			if modifiers[part] {
				return "", ErrInvalidPreferences
			}
			modifiers[part] = true
		case key == "" && isShortcutKey(part):
			key = part
		default:
			return "", ErrInvalidPreferences
		}
	}
	if !modifiers["mod"] || key == "" {
		return "", ErrInvalidPreferences
	}
	canonical := "mod"
	if modifiers["shift"] {
		canonical += "+shift"
	}
	if modifiers["alt"] {
		canonical += "+alt"
	}
	return canonical + "+" + key, nil
}

// isShortcutKey 只接受单个 ASCII 字符：字母、数字或 / , . ; [ ]。
func isShortcutKey(part string) bool {
	if len(part) != 1 {
		return false
	}
	character := part[0]
	return (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || strings.IndexByte("/,.;[]", character) >= 0
}
