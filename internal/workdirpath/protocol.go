package workdirpath

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const Kind = "workspace_path"
const MaxPathBytes = 4096
const Algorithm = "RSA-OAEP-256/A256GCM"

var ErrInvalid = errors.New("workspace path: invalid request")
var ErrUnavailable = errors.New("workspace path: unavailable")

type Scope struct {
	OrganizationID string `json:"organization_id"`
	DeviceID       string `json:"device_id"`
	SpaceID        string `json:"space_id"`
	ConversationID string `json:"conversation_id"`
	AgentID        string `json:"agent_id"`
	RunID          string `json:"run_id"`
	Generation     int64  `json:"generation"`
	UserID         string `json:"user_id"`
}

func (s Scope) Valid() bool {
	for _, value := range []string{s.OrganizationID, s.DeviceID, s.SpaceID, s.ConversationID, s.AgentID, s.RunID, s.UserID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return false
		}
	}
	return s.Generation > 0 && s.Generation <= 9007199254740991
}

type Request struct {
	Scope
	RequestID string `json:"request_id"`
	PublicKey string `json:"public_key"`
	ExpiresAt int64  `json:"expires_at"`
}

func Parse(raw []byte) (Request, error) {
	var request Request
	if len(raw) > 2048 || decode(raw, &request) != nil || !request.Scope.Valid() || request.ExpiresAt <= 0 {
		return Request{}, ErrInvalid
	}
	parsed, err := uuid.Parse(request.RequestID)
	if err != nil || parsed == uuid.Nil || parsed.String() != request.RequestID {
		return Request{}, ErrInvalid
	}
	if _, err := request.key(); err != nil {
		return Request{}, ErrInvalid
	}
	return request, nil
}

func (r Request) key() (*rsa.PublicKey, error) {
	der, err := base64.StdEncoding.Strict().DecodeString(r.PublicKey)
	if err != nil {
		return nil, ErrInvalid
	}
	key, err := x509.ParsePKIXPublicKey(der)
	public, ok := key.(*rsa.PublicKey)
	if err != nil || !ok || public.N.BitLen() != 2048 || public.E != 65537 {
		return nil, ErrInvalid
	}
	return public, nil
}

func (r Request) Live(now time.Time) bool {
	remaining := r.ExpiresAt - now.UnixMilli()
	return remaining > 0 && remaining <= 60000
}

func (r Request) Binding() []byte {
	return []byte(strings.Join([]string{"openfox-workspace-path-v1", r.OrganizationID, r.DeviceID, r.SpaceID,
		r.ConversationID, r.AgentID, r.RunID, strconv.FormatInt(r.Generation, 10), r.UserID,
		r.RequestID, strconv.FormatInt(r.ExpiresAt, 10)}, "\n"))
}

type Envelope struct {
	Algorithm  string `json:"algorithm"`
	WrappedKey string `json:"wrapped_key"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func ValidPath(value string) bool {
	return value != "" && len(value) <= MaxPathBytes && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func Seal(r Request, path string) (Envelope, error) {
	if !ValidPath(path) || !r.Live(time.Now()) {
		return Envelope{}, ErrUnavailable
	}
	public, err := r.key()
	if err != nil {
		return Envelope{}, err
	}
	secret := make([]byte, 32)
	defer clear(secret)
	if _, err := rand.Read(secret); err != nil {
		return Envelope{}, ErrUnavailable
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return Envelope{}, ErrUnavailable
	}
	authenticated, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, ErrUnavailable
	}
	nonce := make([]byte, authenticated.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, ErrUnavailable
	}
	binding := r.Binding()
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, public, secret, binding)
	if err != nil {
		return Envelope{}, ErrUnavailable
	}
	ciphertext := authenticated.Seal(nil, nonce, []byte(path), binding)
	return Envelope{Algorithm, base64.StdEncoding.EncodeToString(wrapped), base64.StdEncoding.EncodeToString(nonce), base64.StdEncoding.EncodeToString(ciphertext)}, nil
}

func ValidateEnvelope(raw []byte) error {
	var envelope Envelope
	if decode(raw, &envelope) != nil || envelope.Algorithm != Algorithm {
		return ErrInvalid
	}
	for _, value := range []struct {
		encoded          string
		minimum, maximum int
	}{
		{envelope.WrappedKey, 256, 256}, {envelope.Nonce, 12, 12}, {envelope.Ciphertext, 17, MaxPathBytes + 16},
	} {
		decoded, err := base64.StdEncoding.Strict().DecodeString(value.encoded)
		if err != nil || len(decoded) < value.minimum || len(decoded) > value.maximum {
			return ErrInvalid
		}
	}
	return nil
}

func decode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}
