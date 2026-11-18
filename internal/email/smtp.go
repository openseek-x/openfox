package email

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	SecuritySSLTLS   = "ssl_tls"
	SecuritySTARTTLS = "starttls"
)

type SMTPConfig struct {
	Host     string
	Port     int
	Security string
	User     string
	From     string
	Password string
	Timeout  time.Duration
}

type SMTPSender struct {
	Config SMTPConfig
}

func (c SMTPConfig) Validate() error {
	if strings.TrimSpace(c.Host) == "" || c.Port < 1 || c.Port > 65535 || strings.TrimSpace(c.User) == "" || strings.TrimSpace(c.From) == "" || c.Password == "" {
		return errors.New("smtp configuration is incomplete")
	}
	if c.Security != SecuritySSLTLS && c.Security != SecuritySTARTTLS {
		return errors.New("smtp security must be ssl_tls or starttls")
	}
	if strings.ContainsAny(c.Host+c.User+c.From, "\r\n") {
		return errors.New("smtp configuration contains a newline")
	}
	if !validAddress(c.From) {
		return errors.New("smtp from must be an email address")
	}
	if c.Timeout <= 0 {
		return errors.New("smtp timeout must be positive")
	}
	return nil
}

func (s SMTPSender) Send(ctx context.Context, to, subject, body string) error {
	if ctx == nil {
		return errors.New("smtp send context is nil")
	}
	if err := s.Config.Validate(); err != nil {
		return err
	}
	if !validAddress(to) || strings.ContainsAny(to+subject, "\r\n") {
		return errors.New("smtp message header is invalid")
	}
	deadline := time.Now().Add(s.Config.Timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	address := net.JoinHostPort(s.Config.Host, strconv.Itoa(s.Config.Port))
	dialer := &net.Dialer{Timeout: s.Config.Timeout}
	var conn net.Conn
	var err error
	if s.Config.Security == SecuritySSLTLS {
		conn, err = (&tls.Dialer{
			NetDialer: dialer,
			Config:    &tls.Config{ServerName: s.Config.Host, MinVersion: tls.VersionTLS12},
		}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	session := newSession(conn)
	if _, err := session.response(220); err != nil {
		return err
	}
	capabilities, err := session.command(250, "EHLO openfox")
	if err != nil {
		return err
	}
	if s.Config.Security == SecuritySTARTTLS {
		if !hasExtension(capabilities, "STARTTLS") {
			return errors.New("smtp server does not support starttls")
		}
		if _, err := session.command(220, "STARTTLS"); err != nil {
			return err
		}
		tlsConn := tls.Client(conn, &tls.Config{ServerName: s.Config.Host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = tlsConn
		_ = conn.SetDeadline(deadline)
		session = newSession(conn)
		capabilities, err = session.command(250, "EHLO openfox")
		if err != nil {
			return err
		}
	}
	if err := session.authenticate(capabilities, s.Config.User, s.Config.Password); err != nil {
		return err
	}
	if _, err := session.command(250, "MAIL FROM:<"+s.Config.From+">"); err != nil {
		return err
	}
	if _, err := session.command(250, "RCPT TO:<"+to+">"); err != nil {
		return err
	}
	if _, err := session.command(354, "DATA"); err != nil {
		return err
	}
	if err := session.message(s.Config.From, to, subject, body); err != nil {
		return err
	}
	_, err = session.command(221, "QUIT")
	if err != nil {
		// Some providers close immediately after accepting DATA. A failed QUIT
		// is not allowed to turn an accepted message into a reported failure.
		return nil
	}
	return nil
}

type smtpSession struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

func newSession(conn net.Conn) *smtpSession {
	return &smtpSession{conn: conn, reader: bufio.NewReader(conn), writer: bufio.NewWriter(conn)}
}

func (s *smtpSession) command(expected int, command string) (string, error) {
	if _, err := fmt.Fprintf(s.writer, "%s\r\n", command); err != nil {
		return "", err
	}
	if err := s.writer.Flush(); err != nil {
		return "", err
	}
	return s.response(expected)
}

func (s *smtpSession) response(expected int) (string, error) {
	var message strings.Builder
	var code int
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 3 {
			return "", errors.New("smtp response is malformed")
		}
		parsed, err := strconv.Atoi(line[:3])
		if err != nil {
			return "", errors.New("smtp response code is malformed")
		}
		if code == 0 {
			code = parsed
		}
		if message.Len() > 0 {
			message.WriteByte('\n')
		}
		if len(line) > 4 {
			message.WriteString(line[4:])
		}
		if len(line) < 4 || line[3] == ' ' {
			break
		}
	}
	if expected != 0 && code != expected {
		return message.String(), fmt.Errorf("smtp response %d: %s", code, message.String())
	}
	return message.String(), nil
}

func (s *smtpSession) authenticate(capabilities, user, password string) error {
	if !hasExtension(capabilities, "AUTH") {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + password))
	if strings.Contains(strings.ToUpper(capabilities), "AUTH PLAIN") {
		_, err := s.command(235, "AUTH PLAIN "+encoded)
		return err
	}
	if strings.Contains(strings.ToUpper(capabilities), "AUTH LOGIN") {
		if _, err := s.command(334, "AUTH LOGIN"); err != nil {
			return err
		}
		if _, err := s.command(334, base64.StdEncoding.EncodeToString([]byte(user))); err != nil {
			return err
		}
		_, err := s.command(235, base64.StdEncoding.EncodeToString([]byte(password)))
		return err
	}
	return errors.New("smtp server does not support a supported auth mechanism")
}

func (s *smtpSession) message(from, to, subject, body string) error {
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", from, to, subject, body)
	for _, line := range strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		if _, err := fmt.Fprintf(s.writer, "%s\r\n", line); err != nil {
			return err
		}
	}
	if _, err := s.writer.WriteString(".\r\n"); err != nil {
		return err
	}
	if err := s.writer.Flush(); err != nil {
		return err
	}
	_, err := s.response(250)
	return err
}

func hasExtension(capabilities, extension string) bool {
	for _, line := range strings.Split(strings.ToUpper(capabilities), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == strings.ToUpper(extension) {
			return true
		}
	}
	return false
}

func validAddress(value string) bool {
	if len(value) > 320 || strings.ContainsAny(value, " \t\r\n") || strings.Count(value, "@") != 1 {
		return false
	}
	parts := strings.SplitN(value, "@", 2)
	return parts[0] != "" && strings.Contains(parts[1], ".")
}
