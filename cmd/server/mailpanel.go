package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/liomix/astro-liomix-server/internal/admin"
	"github.com/liomix/astro-liomix-server/internal/platform/mail"
)

// mailPanel adapts the settings store to the console.
//
// It is the only place that can turn a form into an SMTP account, which is
// exactly the power the old read-only page refused to have: configuring mail is
// an administrator's job, and pretending otherwise meant every change needed a
// redeploy.
type mailPanel struct {
	store    *mail.Store
	siteName string
	logger   *slog.Logger
	timeout  time.Duration
}

var _ admin.MailService = mailPanel{}

// Settings implements admin.MailService. The stored password is replaced by a
// yes/no answer: the page has no use for the value and every reason not to see
// it.
func (p mailPanel) Settings(ctx context.Context) (admin.MailSettings, error) {
	settings, _, err := p.store.Load(ctx)
	if err != nil {
		return admin.MailSettings{}, err
	}
	return admin.MailSettings{
		Host:          settings.Host,
		Port:          settings.Port,
		TLS:           settings.TLS,
		Username:      settings.Username,
		HasPassword:   settings.Password != "",
		From:          settings.From,
		FromName:      settings.FromName,
		AdminTo:       settings.AdminTo,
		ReplyNotify:   settings.ReplyNotify,
		AdminNotify:   settings.AdminNotify,
		TemplateAdmin: settings.TemplateAdmin,
		TemplateReply: settings.TemplateReply,
	}, nil
}

// SaveSettings implements admin.MailService.
func (p mailPanel) SaveSettings(ctx context.Context, in admin.MailSettings) error {
	current, found, err := p.store.Load(ctx)
	if err != nil {
		return err
	}
	next := mail.Settings{
		Host:          in.Host,
		Port:          in.Port,
		TLS:           in.TLS,
		Username:      in.Username,
		From:          in.From,
		FromName:      in.FromName,
		AdminTo:       in.AdminTo,
		ReplyNotify:   in.ReplyNotify,
		AdminNotify:   in.AdminNotify,
		TemplateAdmin: in.TemplateAdmin,
		TemplateReply: in.TemplateReply,
	}
	// An empty box means "leave it alone": the page was never given the stored
	// value, so it cannot send it back, and reading empty as "erase" would wipe
	// the credential every time somebody edited the port.
	switch {
	case in.Password != "":
		next.Password = in.Password
	case found:
		next.Password = current.Password
	}
	return p.store.Save(ctx, next)
}

// SendTest implements admin.MailService.
//
// It dials and delivers before answering, so a rejected password surfaces as a
// message on the page. The alternative — queueing it like a notification —
// would report success for a message that never left the building.
func (p mailPanel) SendTest(ctx context.Context, to string) error {
	settings, _, err := p.store.Load(ctx)
	if err != nil {
		return err
	}
	if !settings.Configured() {
		return errors.New("mail: not configured")
	}
	sender, err := mail.NewSMTPSender(settings.SMTP())
	if err != nil {
		return err
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	site := p.siteName
	if site == "" {
		site = "站点"
	}
	message := mail.Message{
		To:      to,
		Subject: "[" + site + "] 测试邮件",
		Text: "这是一封测试邮件。\n\n如果你收到了它，说明邮件设置可用，" +
			"通过审核的评论会按开关发出通知。\n",
		HTML: `<div style="font-family:-apple-system,'Segoe UI',Roboto,Arial,sans-serif;font-size:14px;line-height:1.7;color:#1f2328">` +
			`<p>这是一封测试邮件。</p><p>如果你收到了它，说明邮件设置可用，通过审核的评论会按开关发出通知。</p></div>`,
	}
	if err := sender.Send(ctx, message); err != nil {
		return fmt.Errorf("send test mail: %w", err)
	}
	p.logger.InfoContext(ctx, "sent test mail", slog.String("to", to))
	return nil
}
