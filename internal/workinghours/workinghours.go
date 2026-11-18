// Package workinghours computes weekly availability on the real UTC timeline.
// It owns no clocks, database access, or execution authorization.
package workinghours

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

type Trigger string

const (
	Legacy         Trigger = "LEGACY"
	Assignment     Trigger = "ASSIGNMENT"
	CommentMention Trigger = "COMMENT_MENTION"
	DirectChat     Trigger = "DIRECT_CHAT"
	Other          Trigger = "OTHER"
)

var ErrInvalid = errors.New("working hours: invalid configuration")
var ErrNoOccurrence = errors.New("working hours: no occurrence within search horizon")

type Period struct {
	Start string `json:"start"`
	End   string `json:"end"`
}
type Triggers struct {
	Assignment     bool `json:"assignment"`
	CommentMention bool `json:"comment_mention"`
	DirectChat     bool `json:"direct_chat"`
}
type Config struct {
	Enabled  bool       `json:"enabled"`
	Timezone string     `json:"timezone"`
	Days     [][]Period `json:"days"`
	Triggers Triggers   `json:"triggers"`
}

// Parse rejects missing/null booleans as well as unknown keys. An absent or null
// config means disabled; callers preserve omitted updates before storing them.
func Parse(raw []byte) (*Config, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var in struct {
		Enabled  *bool      `json:"enabled"`
		Timezone string     `json:"timezone"`
		Days     [][]Period `json:"days"`
		Triggers *struct {
			Assignment     *bool `json:"assignment"`
			CommentMention *bool `json:"comment_mention"`
			DirectChat     *bool `json:"direct_chat"`
		} `json:"triggers"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || in.Enabled == nil || in.Triggers == nil || in.Triggers.Assignment == nil || in.Triggers.CommentMention == nil || in.Triggers.DirectChat == nil {
		return nil, ErrInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, ErrInvalid
	}
	for _, day := range in.Days {
		if day == nil {
			return nil, ErrInvalid
		}
	}
	c := &Config{Enabled: *in.Enabled, Timezone: in.Timezone, Days: in.Days, Triggers: Triggers{*in.Triggers.Assignment, *in.Triggers.CommentMention, *in.Triggers.DirectChat}}
	if _, err := c.location(); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *Config) location() (*time.Location, error) {
	if c.Timezone == "" || c.Timezone == "Local" || len(c.Days) != 7 {
		return nil, ErrInvalid
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, ErrInvalid
	}
	for _, day := range c.Days {
		if len(day) > 2 {
			return nil, ErrInvalid
		}
		for i, p := range day {
			start, ok := minute(p.Start, false)
			if !ok {
				return nil, ErrInvalid
			}
			end, ok := minute(p.End, true)
			if !ok || start >= end {
				return nil, ErrInvalid
			}
			for _, prior := range day[:i] {
				a, _ := minute(prior.Start, false)
				b, _ := minute(prior.End, true)
				if start < b && a < end {
					return nil, ErrInvalid
				}
			}
		}
	}
	return loc, nil
}
func minute(s string, end bool) (int, bool) {
	if end && s == "24:00" {
		return 1440, true
	}
	if len(s) != 5 || s[2] != ':' || s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' || s[3] < '0' || s[3] > '9' || s[4] < '0' || s[4] > '9' {
		return 0, false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	return h*60 + m, h < 24 && m < 60
}
func (c *Config) Applies(kind Trigger) bool {
	if c == nil || !c.Enabled {
		return false
	}
	switch kind {
	case Assignment:
		return c.Triggers.Assignment
	case CommentMention:
		return c.Triggers.CommentMention
	case DirectChat:
		return c.Triggers.DirectChat
	}
	return false
}

// Next returns the first admissible instant at/after both now and requested.
// nil means an explicitly closed whole week; it never means unrestricted.
// Local intervals are half-open; skipped DST minutes do not exist, while both
// occurrences of a repeated minute are valid. Already-open instants retain nanos.
func Next(c *Config, kind Trigger, now time.Time, requested *time.Time) (*time.Time, error) {
	at := now.UTC()
	if requested != nil && requested.After(at) {
		at = requested.UTC()
	}
	if !c.Applies(kind) {
		return &at, nil
	}
	loc, err := c.location()
	if err != nil {
		return nil, err
	}
	open := false
	for _, day := range c.Days {
		open = open || len(day) > 0
	}
	if !open {
		return nil, nil
	}
	matches := func(t time.Time) bool {
		local := t.In(loc)
		day := (int(local.Weekday()) + 6) % 7
		m := local.Hour()*60 + local.Minute()
		for _, p := range c.Days[day] {
			a, _ := minute(p.Start, false)
			b, _ := minute(p.End, true)
			if m >= a && m < b {
				return true
			}
		}
		return false
	}
	if matches(at) {
		return &at, nil
	}
	// Two weeks plus two days also covers a local date skipped by an IANA offset
	// change; a weekly interval swallowed by spring DST is found the next week.
	stop := at.Add(16 * 24 * time.Hour)
	for candidate := at.Truncate(time.Minute).Add(time.Minute); !candidate.After(stop); candidate = candidate.Add(time.Minute) {
		if matches(candidate) {
			return &candidate, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNoOccurrence, c.Timezone)
}
