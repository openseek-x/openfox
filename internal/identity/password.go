package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidPasswordHash = errors.New("identity: invalid password hash")
	ErrInvalidCredentials  = errors.New("identity: invalid credentials")
)

type Argon2idParams struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	KeyLength   uint32
	SaltLength  uint32
}

type Argon2idHasher struct {
	params Argon2idParams
}

func NewArgon2idHasher(params Argon2idParams) (Argon2idHasher, error) {
	if params.MemoryKiB < 16*1024 || params.MemoryKiB > 1024*1024 ||
		params.Iterations < 1 || params.Iterations > 10 ||
		params.Parallelism < 1 || params.Parallelism > 16 ||
		params.KeyLength < 16 || params.KeyLength > 64 ||
		params.SaltLength < 16 || params.SaltLength > 64 {
		return Argon2idHasher{}, errors.New("identity: invalid Argon2id parameters")
	}
	return Argon2idHasher{params: params}, nil
}

func (h Argon2idHasher) Hash(password string) (string, error) {
	if h.params.SaltLength == 0 {
		return "", errors.New("identity: uninitialized password hasher")
	}
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, h.params.KeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		h.params.MemoryKiB,
		h.params.Iterations,
		h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func (h Argon2idHasher) Verify(password, encoded string) error {
	if h.params.SaltLength == 0 {
		return ErrInvalidPasswordHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return ErrInvalidPasswordHash
	}
	memory, iterations, parallelism, err := parseArgon2Params(parts[3])
	if err != nil || !validArgon2Costs(memory, iterations, parallelism) {
		return ErrInvalidPasswordHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return ErrInvalidPasswordHash
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return ErrInvalidPasswordHash
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)))
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return ErrInvalidCredentials
	}
	return nil
}

func parseArgon2Params(value string) (uint32, uint32, uint8, error) {
	var memory, iterations uint32
	var parallelism uint8
	seen := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 || seen[parts[0]] {
			return 0, 0, 0, ErrInvalidPasswordHash
		}
		seen[parts[0]] = true
		switch parts[0] {
		case "m":
			parsed, err := strconv.ParseUint(parts[1], 10, 32)
			if err != nil {
				return 0, 0, 0, ErrInvalidPasswordHash
			}
			memory = uint32(parsed)
		case "t":
			parsed, err := strconv.ParseUint(parts[1], 10, 32)
			if err != nil {
				return 0, 0, 0, ErrInvalidPasswordHash
			}
			iterations = uint32(parsed)
		case "p":
			parsed, err := strconv.ParseUint(parts[1], 10, 8)
			if err != nil {
				return 0, 0, 0, ErrInvalidPasswordHash
			}
			parallelism = uint8(parsed)
		default:
			return 0, 0, 0, ErrInvalidPasswordHash
		}
	}
	if len(seen) != 3 || memory == 0 || iterations == 0 || parallelism == 0 {
		return 0, 0, 0, ErrInvalidPasswordHash
	}
	return memory, iterations, parallelism, nil
}

func validArgon2Costs(memory uint32, iterations uint32, parallelism uint8) bool {
	return memory >= 16*1024 && memory <= 1024*1024 &&
		iterations >= 1 && iterations <= 10 && parallelism >= 1 && parallelism <= 16
}

func NewSessionToken() (string, []byte, error) {
	plain := make([]byte, 32)
	if _, err := rand.Read(plain); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(plain)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func HashSessionToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}
