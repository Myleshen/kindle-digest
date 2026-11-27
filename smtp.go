package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wneessen/go-mail"
)

const (
	smtpTimeout  = 30 * time.Second
	sendInterval = 5 * time.Second
)

type mailer struct {
	cfg      *Config
	log      *logger
	interval time.Duration
}

// send emails one message per part, sequentially, with a pause between sends.
func (m *mailer) send(ctx context.Context, parts []*part, dateLabel string) error {
	subject := "Daily Digest — " + dateLabel
	body := digestBody(parts, dateLabel)

	for i, pt := range parts {
		if err := m.sendOne(ctx, pt, subject, body); err != nil {
			return fmt.Errorf("sending part %d: %w", i+1, err)
		}
		m.log.info("sent part %d/%d: %s (%d bytes)", i+1, len(parts), filepath.Base(pt.path), pt.size)
		if i < len(parts)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(m.interval):
			}
		}
	}
	return nil
}

func (m *mailer) sendOne(ctx context.Context, pt *part, subject, body string) error {
	if _, err := os.Stat(pt.path); err != nil {
		return fmt.Errorf("attachment %s: %w", pt.path, err)
	}
	msg, err := newDigestMessage(m.cfg, pt, subject, body)
	if err != nil {
		return err
	}

	client, err := mail.NewClient(m.cfg.SMTPHost,
		mail.WithPort(m.cfg.SMTPPort),
		mail.WithSMTPAuth(mail.SMTPAuthAutoDiscover),
		mail.WithUsername(m.cfg.SMTPUsername),
		mail.WithPassword(m.cfg.SMTPPassword),
		mail.WithTLSPolicy(mail.TLSMandatory),
		mail.WithTimeout(smtpTimeout),
	)
	if err != nil {
		return fmt.Errorf("creating SMTP client: %w", err)
	}
	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("SMTP delivery: %w", err)
	}
	return nil
}

func newDigestMessage(cfg *Config, pt *part, subject, body string) (*mail.Msg, error) {
	msg := mail.NewMsg()
	if err := msg.From(cfg.SMTPFrom); err != nil {
		return nil, fmt.Errorf("setting From: %w", err)
	}
	if err := msg.To(cfg.KindleEmail); err != nil {
		return nil, fmt.Errorf("setting To: %w", err)
	}
	msg.Subject(subject)
	msg.SetBodyString(mail.TypeTextPlain, body)
	// AttachFile keeps the base name, which partFilename guarantees is pure ASCII.
	msg.AttachFile(pt.path)
	if len(msg.GetAttachments()) != 1 {
		return nil, fmt.Errorf("attaching %s failed", pt.path)
	}
	return msg, nil
}

func digestBody(parts []*part, dateLabel string) string {
	totalEntries := 0
	for _, pt := range parts {
		totalEntries += len(pt.entries)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Daily Digest — %s\n\n", dateLabel)
	fmt.Fprintf(&b, "%d entries in %d %s.\n\n", totalEntries, len(parts), plural(len(parts), "part", "parts"))
	for i, pt := range parts {
		fmt.Fprintf(&b, "Part %d: %s (%d %s)\n",
			i+1, filepath.Base(pt.path), len(pt.entries), plural(len(pt.entries), "entry", "entries"))
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
