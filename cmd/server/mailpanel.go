package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/liomix/astro-liomix-server/internal/admin"
	"github.com/liomix/astro-liomix-server/internal/platform/config"
	"github.com/liomix/astro-liomix-server/internal/platform/mail"
)

// mailPanel is the mail subsystem as the console sees it.
//
// It exposes configuration for display and a way to send one test message. It
// deliberately cannot change the SMTP settings: those are environment, and an
// operator who could edit them from a web page could also point the server at a
// mail host of their choosing.
type mailPanel struct {
	cfg     *config.Config
	queue   *mail.Queue
	logger  *slog.Logger
	timeout time.Duration
}

var _ admin.MailService = mailPanel{}

// Info implements admin.MailService.
func (p mailPanel) Info() admin.MailInfo {
	return admin.MailInfo{
		Enabled: p.cfg.Mail.Enabled,
		Host:    p.cfg.Mail.Host,
		Port:    p.cfg.Mail.Port,
		TLS:     p.cfg.Mail.TLS,
		From:    p.cfg.Mail.From,
		Admin:   p.cfg.Mail.Admin,
	}
}

// SendTest queues a message so the operator can confirm the SMTP settings
// without waiting for a real comment.
func (p mailPanel) SendTest(_ context.Context, to string) error {
	if !p.cfg.Mail.Enabled {
		return fmt.Errorf("mail: not enabled")
	}
	message := mail.Message{
		To:      to,
		Subject: "[" + p.cfg.Mail.SiteName + "] 测试邮件",
		Text:    "这是一封测试邮件。\n\n如果你收到了它，说明 SMTP 配置可用。\n",
		HTML: `<div style="font-family:-apple-system,'Segoe UI',Roboto,Arial,sans-serif;font-size:14px;line-height:1.7;color:#1f2328">` +
			`<p>这是一封测试邮件。</p><p>如果你收到了它，说明 SMTP 配置可用。</p></div>`,
	}
	if !p.queue.Enqueue(message) {
		return fmt.Errorf("mail: queue is full or closed")
	}
	return nil
}
