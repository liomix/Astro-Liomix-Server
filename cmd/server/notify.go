package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/mail"
)

// commentNotifier turns an approved comment into notifications.
//
// It does no I/O itself: both messages go into the mail queue and this returns
// immediately. A comment that just got approved must not wait for an SMTP
// handshake, and a mail server that is down must not make the approval look
// like it failed.
//
// The switches are read per message rather than captured at startup, so turning
// a notice off on the page takes effect on the next comment.
type commentNotifier struct {
	queue   *mail.Queue
	logger  *slog.Logger
	site    string
	siteURL string
	// settings reads the current configuration. Nil means "not configured",
	// which is treated as everything off.
	settings func(ctx context.Context) (mail.Settings, error)
}

var _ comment.Notifier = commentNotifier{}

// Notify implements comment.Notifier.
func (n commentNotifier) Notify(ctx context.Context, notification comment.Notification) error {
	c := notification.Comment
	if c == nil {
		return nil
	}

	settings := mail.Settings{}
	if n.settings != nil {
		loaded, err := n.settings(ctx)
		if err != nil {
			n.logger.WarnContext(ctx, "read mail settings", slog.Any("error", err))
			return nil
		}
		settings = loaded
	}
	// A half-configured account is not worth a dial attempt per comment.
	if !settings.Configured() {
		return nil
	}

	notice := mail.CommentNotice{
		SiteName:    n.site,
		SiteURL:     n.siteURL,
		PostSlug:    c.PostSlug,
		AuthorName:  c.Author.Name,
		AuthorEmail: c.Author.Email,
		Body:        c.Body,
		CreatedAt:   c.CreatedAt,
	}

	if settings.AdminNotify {
		for _, to := range settings.AdminTo {
			message := mail.WithCustomBody(mail.NewCommentForAdmin(notice), settings.TemplateAdmin, notice)
			message.To = to
			if !n.queue.Enqueue(message) {
				n.logger.DebugContext(ctx, "admin notification dropped",
					slog.String("comment_id", c.ID), slog.String("to", redactRecipient(to)))
			}
		}
	}

	if !settings.ReplyNotify || notification.Parent == nil {
		return nil
	}
	parent := notification.Parent
	to := strings.TrimSpace(parent.Author.Email)
	if to == "" {
		return nil
	}
	// Someone replying to themselves does not need an email about it, and a
	// reply from the same address is usually the same person on another device.
	if strings.EqualFold(to, strings.TrimSpace(c.Author.Email)) {
		return nil
	}

	notice.ParentAuthor = parent.Author.Name
	message := mail.WithCustomBody(mail.NewReply(notice), settings.TemplateReply, notice)
	message.To = to
	if !n.queue.Enqueue(message) {
		n.logger.DebugContext(ctx, "reply notification dropped", slog.String("comment_id", c.ID))
	}
	return nil
}

// redactRecipient keeps a log line useful without recording who reads the blog.
func redactRecipient(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		return "***"
	}
	return addr[:1] + "***" + addr[at:]
}
