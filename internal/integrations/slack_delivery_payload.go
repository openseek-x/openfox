package integrations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"openfox/internal/integrations/slack"
)

const slackDeliveryEvent = "openfox_delivery"

type slackDeliveryBody struct {
	Text string `json:"text"`
}

// slackTextParts budgets the text after Slack entity escaping, without splitting
// an entity or Unicode code point. The last part always includes the protected
// source link. A visible ellipsis marks the existing 8,000-rune/body or 10-part
// display limit; full content remains in OpenFox.
func slackTextParts(body, link string, maxParts int) ([]string, error) {
	if maxParts < 1 || maxParts > 10 || !utf8.ValidString(body) {
		return nil, ErrInvalidRequest
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || len(link) > 2048 {
		return nil, ErrInvalidRequest
	}
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" || strings.IndexFunc(body, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
		return nil, ErrInvalidRequest
	}
	suffix := "\n\n" + link
	suffixCost := utf8.RuneCountInString(slack.EscapeText(suffix))
	if suffixCost >= slack.MaxMessageRunes {
		return nil, ErrInvalidRequest
	}
	runes := []rune(body)
	if len(runes) > 8000 {
		runes = append(runes[:8000:8000], '…')
	}
	var parts []string
	for {
		remaining := utf8.RuneCountInString(slack.EscapeText(string(runes)))
		if remaining+suffixCost <= slack.MaxMessageRunes {
			return append(parts, string(runes)+suffix), nil
		}
		limit := slack.MaxMessageRunes
		last := len(parts) == maxParts-1
		if last {
			limit -= suffixCost + 1 // Reserve a visible truncation marker.
		}
		used, end := 0, 0
		for end < len(runes) && used+slackRuneCost(runes[end]) <= limit {
			used += slackRuneCost(runes[end])
			end++
		}
		if last {
			return append(parts, string(runes[:end])+"…"+suffix), nil
		}
		parts = append(parts, string(runes[:end]))
		runes = runes[end:]
	}
}

func slackRuneCost(r rune) int {
	switch r {
	case '&':
		return 5
	case '<', '>':
		return 4
	default:
		return 1
	}
}

func slackPartRequest(channel, thread, marker string, part int, body slackDeliveryBody) slack.PostMessageInput {
	bodyHash := sha256.Sum256([]byte(body.Text))
	return slack.PostMessageInput{Channel: channel, ThreadTS: thread, Text: body.Text, Metadata: &slack.MessageMetadata{
		EventType: slackDeliveryEvent, EventPayload: map[string]string{"delivery_id": marker, "part": strconv.Itoa(part), "body_hash": hex.EncodeToString(bodyHash[:])},
	}}
}
func slackPartHash(input slack.PostMessageInput) []byte {
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	return sum[:]
}

type slackReceiptTarget struct {
	AppID, BotID, BotUserID, TeamID string
	ChannelID, ThreadTS, Marker     string
	Part                            int
	Body                            slackDeliveryBody
}

// Both event and read-back callers must supply the authenticated installation's
// team/channel; optional message fields alone cannot establish those facts.
// Slack may rewrite Unicode emoji into :colon: text on read-back. The bound
// body hash proves the original POST intention, not unchanged remote rendering.
func slackReceiptMatches(target slackReceiptTarget, team, channel string, message slack.Message) bool {
	if team != target.TeamID || channel != target.ChannelID || target.BotID == "" ||
		message.AppID != target.AppID || message.BotID != target.BotID ||
		(message.User != "" && message.User != target.BotUserID) ||
		(message.Team != "" && message.Team != target.TeamID) ||
		message.ThreadTS != target.ThreadTS || message.TS == "" ||
		message.Type != "message" || (message.Subtype != "" && message.Subtype != "bot_message") ||
		message.Metadata == nil {
		return false
	}
	m := message.Metadata
	bodyHash := sha256.Sum256([]byte(target.Body.Text))
	return m.EventType == slackDeliveryEvent && len(m.EventPayload) == 3 && m.EventPayload["body_hash"] == hex.EncodeToString(bodyHash[:]) &&
		m.EventPayload["delivery_id"] == target.Marker && m.EventPayload["part"] == strconv.Itoa(target.Part)
}
