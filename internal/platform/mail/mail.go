// Package mail sends notification email.
//
// It is deliberately small and dependency-free: one SMTP client, one worker
// queue, and templates for the two messages this service sends. Everything a
// caller can observe is non-blocking — a comment must never wait for a
// handshake with a mail server, and a mail server that is down must never make
// a comment fail.
package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/id"
)

// Message is one rendered email. Both bodies are optional; a message with only
// HTML is sent as such, and a message with only text is sent as plain text.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a message, or explains why it could not.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SMTPConfig is where and how to connect. It mirrors config.Mail without
// importing it: this package should be usable from a test with four fields.
type SMTPConfig struct {
	Host string
	Port int
	// TLS is "auto", "starttls", "implicit" or "none". Auto picks implicit TLS
	// on port 465 and STARTTLS everywhere else, which is what the common
	// providers expect.
	TLS      string
	Username string
	Password string
	// From is the envelope sender; FromName is the display name. A name with
	// non-ASCII characters is RFC 2047 encoded.
	From     string
	FromName string
	// Timeout bounds one delivery attempt, including the TLS handshake.
	Timeout time.Duration
}

// SMTPSender delivers over SMTP.
type SMTPSender struct {
	cfg     SMTPConfig
	timeout time.Duration
}

// NewSMTPSender validates the connection settings and returns a sender. It is
// the caller's job to decide *whether* to send; this only answers "can I".
func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("mail: host is required")
	}
	if cfg.Port <= 0 {
		return nil, errors.New("mail: port must be positive")
	}
	if _, _, err := net.SplitHostPort(net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))); err != nil {
		return nil, fmt.Errorf("mail: host %q: %w", cfg.Host, err)
	}
	if cfg.From == "" {
		return nil, errors.New("mail: from address is required")
	}
	switch cfg.TLS {
	case "", "auto", "starttls", "implicit", "none":
	default:
		return nil, fmt.Errorf("mail: unknown TLS mode %q", cfg.TLS)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &SMTPSender{cfg: cfg, timeout: timeout}, nil
}

// Send delivers one message.
//
// The connection is built by hand rather than through smtp.SendMail because
// SendMail cannot do implicit TLS (port 465) and has no timeout at all, which
// is exactly the pair of problems that turns "the mail server hangs" into "the
// comment endpoint hangs".
func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))

	dialer := &net.Dialer{Timeout: s.timeout}
	var (
		conn net.Conn
		err  error
	)
	if s.implicitTLS() {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, s.tlsConfig())
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", addr, err)
	}
	defer conn.Close()

	// A deadline rather than a context: net/smtp does not take one, and a
	// server that stops answering mid-DATA would otherwise hold the worker
	// forever.
	deadline := time.Now().Add(s.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("mail: set deadline: %w", err)
	}

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("mail: greet %s: %w", s.cfg.Host, err)
	}
	defer client.Close()

	if !s.implicitTLS() && s.mode() != "none" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(s.tlsConfig()); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		} else if s.mode() == "starttls" {
			// Configured explicitly, so silence about it would be a downgrade.
			return errors.New("mail: server does not offer STARTTLS")
		}
	}

	if s.cfg.Username != "" {
		// PlainAuth refuses to send credentials over a cleartext connection
		// unless the host is localhost, which is the safety net we want.
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}

	if err := client.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := client.Rcpt(m.To); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	body, err := render(m, s.cfg)
	if err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := writer.Write(body); err != nil {
		return fmt.Errorf("mail: write body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("mail: finish body: %w", err)
	}
	return client.Quit()
}

func (s *SMTPSender) mode() string {
	switch s.cfg.TLS {
	case "starttls", "implicit", "none":
		return s.cfg.TLS
	default:
		if s.cfg.Port == 465 {
			return "implicit"
		}
		return "starttls"
	}
}

func (s *SMTPSender) implicitTLS() bool { return s.mode() == "implicit" }

func (s *SMTPSender) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
}

// NoopSender accepts everything and sends nothing. It is what a server without
// mail configured uses, so callers never have to check for nil.
type NoopSender struct{}

// Send implements Sender.
func (NoopSender) Send(context.Context, Message) error { return nil }

// Queue is an asynchronous sender: Enqueue never blocks on the network.
//
// The queue is bounded on purpose. An unbounded one turns a slow mail server
// into a memory leak, and the correct behaviour when notifications pile up is
// to drop them with a log line — the comment itself already succeeded.
type Queue struct {
	sender Sender
	logger *slog.Logger

	// mu guards closed and the send below. Without it, Close and Enqueue race
	// and a message can be sent on a channel that was just closed — a panic,
	// not a dropped notification.
	mu     sync.RWMutex
	closed bool

	jobs chan Message
	done chan struct{}
	wg   sync.WaitGroup
}

// NewQueue starts one worker and returns the queue. Close stops it.
func NewQueue(sender Sender, size int, logger *slog.Logger) *Queue {
	if sender == nil {
		sender = NoopSender{}
	}
	if size <= 0 {
		size = 128
	}
	if logger == nil {
		logger = slog.Default()
	}
	q := &Queue{
		sender: sender,
		logger: logger,
		jobs:   make(chan Message, size),
		done:   make(chan struct{}),
	}
	q.wg.Add(1)
	go q.work()
	return q
}

func (q *Queue) work() {
	defer q.wg.Done()
	for m := range q.jobs {
		if err := q.sender.Send(context.Background(), m); err != nil {
			q.logger.Warn("send notification mail",
				slog.String("to", redactAddress(m.To)),
				slog.Any("error", err))
		}
	}
	close(q.done)
}

// Enqueue queues a message, reporting whether it was accepted.
//
// A false answer means the caller's message was dropped: the queue is full or
// the queue is closed. It is not an error the caller should propagate — a full
// mailbox is not a reason to fail a comment.
func (q *Queue) Enqueue(m Message) bool {
	if m.To == "" {
		return false
	}

	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return false
	}
	select {
	case q.jobs <- m:
		return true
	default:
		q.logger.Warn("notification queue is full, dropping message",
			slog.String("to", redactAddress(m.To)))
		return false
	}
}

// Close stops accepting messages and waits for the queued ones to be sent,
// until ctx expires. Messages still unsent when ctx expires are abandoned: the
// process is going down and a notification is not worth blocking it.
func (q *Queue) Close(ctx context.Context) error {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.jobs)
	}
	q.mu.Unlock()

	finished := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// render builds the RFC 5322 message: headers plus a multipart body when both
// versions exist.
func render(m Message, cfg SMTPConfig) ([]byte, error) {
	var buf strings.Builder

	from := cfg.From
	if cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", encodeHeader(cfg.FromName), cfg.From)
	}
	subject := encodeHeader(m.Subject)

	headers := [][2]string{
		{"From", from},
		{"To", m.To},
		{"Subject", subject},
		{"Date", time.Now().Format(time.RFC1123Z)},
		{"Message-ID", messageID(cfg.From)},
		// Receivers treat a machine-written notice as bulk mail; saying so keeps
		// vacation responders from replying to a noreply address.
		{"Auto-Submitted", "auto-generated"},
		{"MIME-Version", "1.0"},
	}
	for _, h := range headers {
		// A header value with a newline is header injection: refuse rather
		// than sanitise, because a valid message never has one.
		if strings.ContainsAny(h[1], "\r\n") {
			return nil, fmt.Errorf("mail: header %s contains a newline", h[0])
		}
		buf.WriteString(h[0])
		buf.WriteString(": ")
		buf.WriteString(h[1])
		buf.WriteString("\r\n")
	}

	switch {
	case m.HTML != "" && m.Text != "":
		boundary := "console-notification-boundary"
		buf.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
		for _, part := range []struct{ ctype, body string }{
			{"text/plain; charset=UTF-8", m.Text},
			{"text/html; charset=UTF-8", m.HTML},
		} {
			buf.WriteString("--" + boundary + "\r\n")
			writeBody(&buf, part.ctype, part.body)
		}
		buf.WriteString("--" + boundary + "--\r\n")
	case m.HTML != "":
		writeBody(&buf, "text/html; charset=UTF-8", m.HTML)
	case m.Text != "":
		writeBody(&buf, "text/plain; charset=UTF-8", m.Text)
	default:
		return nil, errors.New("mail: message has neither text nor html body")
	}
	return []byte(buf.String()), nil
}

// writeBody appends one MIME body, base64 encoded, ending with CRLF.
//
// base64 rather than 8bit on purpose. It keeps the message 7-bit clean, so
// delivery no longer depends on the server advertising 8BITMIME — a server that
// does not support it is entitled to reject or silently discard the message —
// and it wraps at 76 columns, which keeps a long HTML line clear of the
// 998-character limit every server may enforce.
func writeBody(buf *strings.Builder, contentType, body string) {
	buf.WriteString("Content-Type: " + contentType + "\r\n")
	buf.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(normalizeNewlines(body)))
	for len(encoded) > 76 {
		buf.WriteString(encoded[:76])
		buf.WriteString("\r\n")
		encoded = encoded[76:]
	}
	buf.WriteString(encoded)
	buf.WriteString("\r\n")
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// encodeHeader applies RFC 2047 encoding only when it is needed, so plain ASCII
// subjects stay readable in a raw message.
func encodeHeader(value string) string {
	for _, r := range value {
		if r > 127 {
			return mime.QEncoding.Encode("UTF-8", value)
		}
	}
	return value
}

// messageID gives every message a unique identity.
//
// Receivers use it to spot duplicates and to decide a message is not a stray
// relay attempt; mail without one is the kind that gets filtered without a
// bounce, which is indistinguishable from "it never sent".
func messageID(from string) string {
	domain := "localhost"
	if at := strings.LastIndex(from, "@"); at >= 0 && at < len(from)-1 {
		domain = from[at+1:]
	}
	return "<" + id.RandomGenerator{}.New() + "@" + domain + ">"
}

// redactAddress keeps the domain and the first character, so a log line can be
// matched against a bounce without recording who the reader is.
func redactAddress(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		return "***"
	}
	local, domain := addr[:at], addr[at+1:]
	if len(local) <= 1 {
		return "*@" + domain
	}
	return local[:1] + "***@" + domain
}
