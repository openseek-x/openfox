package wp02_wp03

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/api"
	"openfox/internal/deviceauth"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/requestid"
)

// Each contract gets its own clone of the configured fixture database. No
// lifecycle test can revoke credentials or memberships used by another test.
func init() {
	if os.Getenv("OPENFOX_TEST_DATABASE_URL") != "" {
		HTTPFactory = newPostgresHarness
	}
}

type postgresHarness struct {
	admin, seed *pgx.Conn
	pool        *postgres.Pool
	server      *httptest.Server
	auth        identity.Service
	database    string
	keys        map[string]ed25519.PrivateKey
	csrf        map[string]string
	mailMu      sync.Mutex
	invitations map[string]string
}

var fixtureCredentials struct {
	sync.Once
	password, hash string
	hasher         identity.Argon2idHasher
	err            error
}

func newPostgresHarness(ctx context.Context) (_ HTTPHarness, err error) {
	config, err := pgx.ParseConfig(os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	base := config.Database
	config.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	var raw [12]byte
	if _, err = rand.Read(raw[:]); err != nil {
		admin.Close(ctx)
		return nil, err
	}
	h := &postgresHarness{admin: admin, database: "wp02_contract_" + hex.EncodeToString(raw[:]), keys: map[string]ed25519.PrivateKey{}, csrf: map[string]string{}, invitations: map[string]string{}}
	defer func() {
		if err != nil {
			_ = h.Close()
		}
	}()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{h.database}.Sanitize()+" TEMPLATE "+pgx.Identifier{base}.Sanitize()); err != nil {
		return nil, err
	}
	config.Database = h.database
	h.seed, err = pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	fixtureCredentials.Do(func() {
		fixtureCredentials.password = hex.EncodeToString(raw[:])
		fixtureCredentials.hasher, fixtureCredentials.err = identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
		if fixtureCredentials.err == nil {
			fixtureCredentials.hash, fixtureCredentials.err = fixtureCredentials.hasher.Hash(fixtureCredentials.password)
		}
	})
	if fixtureCredentials.err != nil {
		return nil, fixtureCredentials.err
	}
	if _, err = h.seed.Exec(ctx, `INSERT INTO openfox.password_credentials (user_id,password_hash) SELECT id,$1 FROM openfox.users ON CONFLICT (user_id) DO UPDATE SET password_hash=EXCLUDED.password_hash`, fixtureCredentials.hash); err != nil {
		return nil, err
	}
	for _, device := range []string{DeviceA, DeviceB} {
		public, private, keyErr := ed25519.GenerateKey(rand.Reader)
		if keyErr != nil {
			return nil, keyErr
		}
		if _, err = h.seed.Exec(ctx, `INSERT INTO openfox.device_keys (device_id,key_id,public_key) VALUES ($1::uuid,$2,$3)`, device, DeviceKeyA, []byte(public)); err != nil {
			return nil, err
		}
		h.keys[device] = private
	}
	// ConnString retains its original database; use a parsed URL/keyword DSN
	// with an explicit final dbname override for the application pool.
	dsn := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if parsed, parseErr := url.Parse(dsn); parseErr == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		parsed.Path = "/" + h.database
		dsn = parsed.String()
	} else {
		dsn += " dbname=" + h.database
	}
	h.pool, err = postgres.New(ctx, postgres.Config{URL: dsn, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 5 * time.Second, LockTimeout: time.Second})
	if err != nil {
		return nil, err
	}
	h.auth = identity.Service{Store: identity.PostgresStore{Pool: h.pool}, PasswordHasher: fixtureCredentials.hasher, EmailSender: h, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	h.server = httptest.NewServer(requestid.Middleware(api.NewWithDispatch(h.auth, h.pool, false, nil, api.AuthRateLimits{}, api.DispatchOptions{PublicURL: "http://openfox.test"})))
	return h, nil
}

func (h *postgresHarness) BaseURL() string     { return h.server.URL }
func (h *postgresHarness) RoutePrefix() string { return "/v1" }
func (h *postgresHarness) Close() error {
	if h.server != nil {
		h.server.Close()
	}
	if h.pool != nil {
		h.pool.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if h.seed != nil {
		_ = h.seed.Close(ctx)
	}
	if h.admin == nil {
		return nil
	}
	defer h.admin.Close(ctx)
	_, err := h.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{h.database}.Sanitize())
	return err
}

type bearerTransport string

func (token bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+string(token))
	return http.DefaultTransport.RoundTrip(clone)
}

func (h *postgresHarness) Client(ctx context.Context, spec AuthSpec) (*http.Client, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	switch spec.Kind {
	case AuthNone:
		return client, nil
	case AuthMalformed:
		client.Transport = bearerTransport("malformed-test-session")
		return client, nil
	case AuthDeviceBearer:
		token := spec.BearerToken
		if token == "" {
			service := deviceauth.Service{Store: deviceauth.PostgresStore{Pool: h.pool}, Now: time.Now}
			challenge, err := service.IssueChallenge(ctx, deviceauth.ChallengeRequest{DeviceID: spec.Principal.DeviceID, KeyID: DeviceKeyA})
			if err != nil {
				return nil, err
			}
			signature := ed25519.Sign(h.keys[spec.Principal.DeviceID], deviceauth.CanonicalPayload(challenge.ID, challenge.DeviceID, challenge.KeyID, challenge.Nonce, challenge.Challenge))
			session, err := service.Verify(ctx, deviceauth.VerifyRequest{ChallengeID: challenge.ID, DeviceID: challenge.DeviceID, KeyID: challenge.KeyID, Signature: signature})
			if err != nil {
				return nil, err
			}
			token = session.Token
		}
		client.Transport = bearerTransport(token)
		return client, nil
	case AuthUserBearer:
		session, err := h.auth.Login(ctx, spec.Principal.Email, fixtureCredentials.password, identity.ClientDesktop)
		if err != nil {
			return nil, err
		}
		client.Transport = bearerTransport(session.Token)
		return client, nil
	case AuthUserCookie:
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
		client.Jar = jar
		body, _ := json.Marshal(map[string]string{"email": spec.Principal.Email, "password": fixtureCredentials.password, "client_kind": "web"})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.BaseURL()+"/v1/auth/login", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		if res.StatusCode != http.StatusCreated {
			return nil, fmt.Errorf("fixture login status %d", res.StatusCode)
		}
		u, _ := url.Parse(h.BaseURL())
		for _, cookie := range jar.Cookies(u) {
			if cookie.Name == "openfox_csrf" {
				h.csrf[spec.Principal.ID] = cookie.Value
			}
		}
		return client, nil
	default:
		return nil, fmt.Errorf("unknown authentication kind %q", spec.Kind)
	}
}
func (h *postgresHarness) CSRFToken(_ context.Context, spec AuthSpec) (string, error) {
	return h.csrf[spec.Principal.ID], nil
}

var invitationLink = regexp.MustCompile(`http://openfox\.test/join\?invite=[^\s]+`)

func (h *postgresHarness) Send(_ context.Context, _ string, _ string, body string) error {
	link := invitationLink.FindString(body)
	parsed, err := url.Parse(link)
	if err != nil || link == "" {
		return fmt.Errorf("invitation test email has no link")
	}
	h.mailMu.Lock()
	defer h.mailMu.Unlock()
	h.invitations[parsed.Query().Get("invite")] = parsed.Query().Get("token")
	return nil
}
func (h *postgresHarness) InvitationToken(_ context.Context, id string) (string, error) {
	h.mailMu.Lock()
	defer h.mailMu.Unlock()
	token := h.invitations[id]
	if token == "" {
		return "", fmt.Errorf("invitation was not delivered")
	}
	return token, nil
}
func (h *postgresHarness) ExpireInvitation(ctx context.Context, id string) error {
	_, err := h.seed.Exec(ctx, `UPDATE openfox.invitations SET issued_at=clock_timestamp()-interval '2 days', expires_at=clock_timestamp()-interval '1 day' WHERE id=$1::uuid`, id)
	return err
}
func (h *postgresHarness) ChallengeMaterial(ctx context.Context, id string) (ChallengeMaterial, error) {
	var m ChallengeMaterial
	var nonce, challenge []byte
	err := h.seed.QueryRow(ctx, `SELECT id::text,device_id::text,key_id,nonce,challenge_bytes FROM openfox.device_challenges WHERE id=$1::uuid`, id).Scan(&m.ID, &m.DeviceID, &m.KeyID, &nonce, &challenge)
	if err == nil {
		m.Payload = deviceauth.CanonicalPayload(m.ID, m.DeviceID, m.KeyID, nonce, challenge)
	}
	return m, err
}
func (h *postgresHarness) SignChallenge(_ context.Context, m ChallengeMaterial) ([]byte, error) {
	return ed25519.Sign(h.keys[m.DeviceID], m.Payload), nil
}
func (h *postgresHarness) ExpireChallenge(ctx context.Context, id string) error {
	_, err := h.seed.Exec(ctx, `UPDATE openfox.device_challenges SET created_at=clock_timestamp()-interval '2 hours', expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1::uuid`, id)
	return err
}
