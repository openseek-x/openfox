// Package gitlabproxy relays TLS records between a GitLab client and a specific
// owner's online computer. Device authorization and single-use rendezvous are
// shared across API replicas. The device never handles GitLab credentials.
package gitlabproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/safehttp"
	"openfox/internal/tenancy"
)

var ErrUnavailable = errors.New("gitlab proxy unavailable")

type Binding struct{ Organization, Device, Owner string }
type Broker struct {
	Pool *postgres.Pool
	Hub  *dispatch.Hub
}

func Instance(raw string) (base, address string, err error) {
	u, err := safehttp.CheckURL(raw)
	if err != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(u.Host, "%") || strings.HasSuffix(u.Host, ":") {
		return "", "", ErrUnavailable
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	port := u.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(u.Hostname(), port)
	if len(addr) > 300 {
		return "", "", ErrUnavailable
	}
	return u.String(), addr, nil
}

// Device scope and organization membership must still be valid when a new
// connection is opened, including by an integration background worker.
func Authorized(ctx context.Context, tx pgx.Tx, b Binding) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.device_grants g
 JOIN openfox.physical_devices d ON d.id=g.device_id
 JOIN openfox.organization_members m ON m.organization_id=g.organization_id AND m.user_id=d.owner_user_id
 WHERE g.organization_id=openfox_private.current_organization_id() AND g.device_id=$1::uuid AND d.owner_user_id=$2::uuid
 AND d.status='ACTIVE' AND g.status='ACTIVE' AND m.status='ACTIVE' AND m.role IN ('OWNER','ADMIN')
 AND d.last_seen_at>clock_timestamp()-interval '90 seconds' AND d.capabilities @> '{"gitlab_proxy":true}')`, b.Device, b.Owner).Scan(&ok)
	if err == nil && !ok {
		return ErrUnavailable
	}
	return err
}

type runTx func(context.Context, func(context.Context, pgx.Tx) error) error

func (b Broker) transaction(s tenancy.OrganizationSelection, source tenancy.Source) runTx {
	return func(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
		return b.Pool.RunForOrganization(ctx, s.OrganizationID, source, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return s, nil }), fn)
	}
}

func (b Broker) Client(binding Binding, base string) (*http.Client, error) {
	_, fixed, err := Instance(base)
	if err != nil || b.Pool == nil || b.Hub == nil {
		return nil, ErrUnavailable
	}
	transport := &http.Transport{DisableKeepAlives: true, TLSHandshakeTimeout: 12 * time.Second, ResponseHeaderTimeout: 18 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || !strings.EqualFold(address, fixed) {
				return nil, ErrUnavailable
			}
			return b.dial(ctx, binding, fixed)
		}}
	return &http.Client{Timeout: 25 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func (b Broker) dial(ctx context.Context, bind Binding, address string) (net.Conn, error) {
	tx := b.transaction(tenancy.OrganizationSelection{OrganizationID: bind.Organization, ActorID: "00000000-0000-0000-0000-000000000000", AuthorizationVersion: 1}, tenancy.SourceOrganizationWorker)
	var id string
	err := tx(ctx, func(ctx context.Context, db pgx.Tx) error {
		if err := Authorized(ctx, db, bind); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "gitlab-proxy:"+bind.Device); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `DELETE FROM openfox.gitlab_proxy_streams WHERE organization_id=openfox_private.current_organization_id() AND device_id=$1::uuid AND expires_at<=clock_timestamp()`, bind.Device); err != nil {
			return err
		}
		return db.QueryRow(ctx, `INSERT INTO openfox.gitlab_proxy_streams(organization_id,device_id,owner_id,address)
 SELECT openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3 WHERE
 (SELECT count(*) FROM openfox.gitlab_proxy_streams WHERE organization_id=openfox_private.current_organization_id() AND device_id=$1::uuid)<4 RETURNING id::text`, bind.Device, bind.Owner, address).Scan(&id)
	})
	if err != nil {
		return nil, err
	}
	b.Hub.Publish(bind.Device, dispatch.Wake{Type: "gitlab.proxy", OrganizationID: bind.Organization, CommandID: id})
	client, stream := net.Pipe()
	go func() {
		pump(stream, tx, id, false)
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tx(cleanup, func(ctx context.Context, db pgx.Tx) error {
			_, err := db.Exec(ctx, `DELETE FROM openfox.gitlab_proxy_streams WHERE id=$1::uuid`, id)
			return err
		})
	}()
	return client, nil
}

func (b Broker) Claim(ctx context.Context, s tenancy.OrganizationSelection, id string) (string, error) {
	var owner, address string
	err := b.transaction(s, tenancy.SourceDeviceRequest)(ctx, func(ctx context.Context, db pgx.Tx) error {
		err := db.QueryRow(ctx, `UPDATE openfox.gitlab_proxy_streams p SET claimed=true FROM openfox.device_grants g
 WHERE p.id=$1::uuid AND p.device_id=$2::uuid AND NOT p.claimed AND p.expires_at>clock_timestamp()
 AND g.organization_id=p.organization_id AND g.device_id=p.device_id AND g.status='ACTIVE' AND g.permission_version=$3
 RETURNING p.owner_id::text,p.address`, id, s.ActorID, s.AuthorizationVersion).Scan(&owner, &address)
		if err != nil {
			return err
		}
		return Authorized(ctx, db, Binding{s.OrganizationID, s.ActorID, owner})
	})
	return address, err
}
func (b Broker) Serve(conn net.Conn, s tenancy.OrganizationSelection, id string) {
	pump(conn, b.transaction(s, tenancy.SourceDeviceRequest), id, true)
}

// One bounded slot per direction provides backpressure. Data is taken only
// once under a row lock. End of stream, error or TTL closes both directions.
func pump(conn net.Conn, tx runTx, id string, device bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer conn.Close()
	send, recv := "to_device", "to_center"
	if device {
		send, recv = recv, send
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	wait := func() bool {
		timer := time.NewTimer(30 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
	go func() {
		defer cancel()
		buf := make([]byte, 32768)
		total := 0
		for {
			n, err := conn.Read(buf)
			total += n
			if total > 8<<20 {
				return
			}
			if n > 0 {
				for {
					stored := false
					e := tx(ctx, func(ctx context.Context, db pgx.Tx) error {
						tag, e := db.Exec(ctx, "UPDATE openfox.gitlab_proxy_streams SET "+send+"=$2 WHERE id=$1::uuid AND expires_at>clock_timestamp() AND "+send+" IS NULL", id, buf[:n])
						stored = tag.RowsAffected() == 1
						return e
					})
					if e != nil {
						return
					}
					if stored {
						break
					}
					if !wait() {
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for total := 0; ctx.Err() == nil; {
		var data []byte
		err := tx(ctx, func(ctx context.Context, db pgx.Tx) error {
			// On idle polls leave the row untouched.
			if err := db.QueryRow(ctx, "SELECT "+recv+" FROM openfox.gitlab_proxy_streams WHERE id=$1::uuid AND expires_at>clock_timestamp() FOR UPDATE", id).Scan(&data); err != nil {
				return err
			}
			if data == nil {
				return nil
			}
			_, err := db.Exec(ctx, "UPDATE openfox.gitlab_proxy_streams SET "+recv+"=NULL WHERE id=$1::uuid", id)
			return err
		})
		if err != nil {
			return
		}
		if data == nil {
			if !wait() {
				return
			}
			continue
		}
		total += len(data)
		if total > 8<<20 {
			return
		}
		if n, err := conn.Write(data); err != nil || n != len(data) {
			return
		}
	}
}
