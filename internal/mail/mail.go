// Package mail sends plain-text email through Resend or SMTP. With neither
// configured it writes the message to the log instead, so every flow that
// sends mail still works on a developer's machine.
package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Provider   string // resend, smtp or log
	From       string
	ResendKey  string
	SMTPHost   string
	SMTPPort   string
	SMTPUser   string
	SMTPPass   string
	SMTPSecure bool // true: TLS from the first byte (port 465)
}

type Mailer struct {
	cfg       Config
	client    *http.Client
	resendURL string
}

var emailRe = regexp.MustCompile(`^[^\s@<>,;]+@[^\s@<>,;]+\.[^\s@<>,;]+$`)

// Valid reports whether s looks like one email address. It also refuses line
// breaks, which would let a caller add headers.
func Valid(s string) bool { return len(s) <= 254 && emailRe.MatchString(s) }

func New(cfg Config) *Mailer {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch {
	case cfg.Provider == "resend" && cfg.ResendKey != "" && cfg.From != "":
	case cfg.Provider == "smtp" && cfg.SMTPHost != "" && cfg.From != "":
	default:
		cfg.Provider = "log"
	}
	return &Mailer{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}, resendURL: "https://api.resend.com/emails"}
}

// Mode is "resend", "smtp" or "log".
func (m *Mailer) Mode() string { return m.cfg.Provider }

// Send delivers one message. The returned status is "sent" or "logged".
func (m *Mailer) Send(ctx context.Context, to, subject, text string) (string, error) {
	if !Valid(to) {
		return "", fmt.Errorf("not an email address: %q", to)
	}
	subject = strings.Join(strings.Fields(subject), " ") // no line breaks in a header
	switch m.cfg.Provider {
	case "resend":
		return "sent", m.resend(ctx, to, subject, text)
	case "smtp":
		return "sent", m.smtp(to, subject, text)
	}
	slog.Info("mail not sent, no provider is configured", "to", to, "subject", subject, "body", text)
	return "logged", nil
}

func (m *Mailer) resend(ctx context.Context, to, subject, text string) error {
	body, _ := json.Marshal(map[string]any{"from": m.cfg.From, "to": []string{to}, "subject": subject, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.resendURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.cfg.ResendKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 400))
		return fmt.Errorf("resend: %s: %s", res.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (m *Mailer) smtp(to, subject, text string) error {
	addr := net.JoinHostPort(m.cfg.SMTPHost, m.cfg.SMTPPort)
	msg := []byte("From: " + m.cfg.From + "\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.ReplaceAll(text, "\n", "\r\n") + "\r\n")
	var auth smtp.Auth
	if m.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", m.cfg.SMTPUser, m.cfg.SMTPPass, m.cfg.SMTPHost)
	}
	from := m.cfg.From
	if i := strings.LastIndex(from, "<"); i >= 0 { // "Name <a@b.c>"
		from = strings.TrimSuffix(from[i+1:], ">")
	}
	if !m.cfg.SMTPSecure {
		return smtp.SendMail(addr, auth, from, []string{to}, msg) // upgrades with STARTTLS when offered
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, &tls.Config{ServerName: m.cfg.SMTPHost})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, m.cfg.SMTPHost)
	if err != nil {
		return err
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
