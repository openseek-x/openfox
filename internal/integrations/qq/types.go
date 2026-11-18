// Package qq implements the official QQ Bot token, passive text and signed
// Webhook contracts. It does not log in as a QQ user or implement QR internals.
package qq

import (
	"errors"
	"net/http"
	"time"
)

const (
	Origin       = "https://api.bot.qq.com"
	MaxBodyBytes = 1 << 20
	MaxIDBytes   = 1024
	MaxTextRunes = 8000
)

var (
	ErrInvalid        = errors.New("qq: invalid input")
	ErrSignature      = errors.New("qq: invalid signature")
	ErrAuthentication = errors.New("qq: authentication failed")
	ErrRejected       = errors.New("qq: request rejected")
	ErrRateLimited    = errors.New("qq: rate limited")
	ErrUnavailable    = errors.New("qq: unavailable")
	ErrUnknown        = errors.New("qq: delivery result unknown")
	ErrUnsupported    = errors.New("qq: unsupported event")
)

// Error deliberately excludes upstream text, request URLs and credentials.
type Error struct {
	Kind         error
	Reason       string
	Status       int
	PlatformCode int64
	RetryAfter   time.Duration
}

func (e *Error) Error() string { return "qq: " + e.Reason }
func (e *Error) Unwrap() error { return e.Kind }

// Transport is an internal test seam, never a user-configurable origin.
type Client struct {
	Transport http.RoundTripper
	Now       func() time.Time
}
type Credentials struct{ AppID, AppSecret string }

func (v Credentials) Valid() bool { return ID(v.AppID) && secret(v.AppSecret) }

func (Credentials) String() string   { return "qq.Credentials([redacted])" }
func (Credentials) GoString() string { return "qq.Credentials([redacted])" }

type Token struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (Token) String() string   { return "qq.Token([redacted])" }
func (Token) GoString() string { return "qq.Token([redacted])" }

type Scope string

const (
	C2C   Scope = "C2C"
	Group Scope = "GROUP"
)

type Reply struct {
	Scope         Scope
	DestinationID string
	MessageID     string
	Text          string
	// I14 permits one terminal reply per original message. Never increment on an
	// unknown result; the database reserves this immutable value before sending.
	Sequence int
}
type SentMessage struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
}
