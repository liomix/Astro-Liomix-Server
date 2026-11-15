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
type commentNotifier struct {
	queue   *mail.Queue
	logger  *slog.Logger
	site    string
	siteURL string
	// admin receives a notice for every approved comment. Empty disables it.
	admin string
	// prefs reads the two switches the console owns. They are read per message
	// rather than captured at startup, so flipping one takes effect on the next
	// comment instead of the next deploy. Nil means "off".
	prefs func(ctx context.Context) (replyNotify, adminNotify bool)
}

var _ comment.Notifier = commentNotifier{}

// Notify implements comment.Notifier.
func (n commentNotifier) Notify(ctx context.Context, notification comment.Notification) error {
	c := notification.Comment
	if c == nil {
		return nil
	}

	notice := mail.CommentNotice{
		SiteName:   n.site,
		SiteURL:    n.siteURL,
		PostSlug:   c.PostSlug,
		AuthorName: c.Author.Name,
		Body:       c.Body,
	}

	replyNotify, adminNotify := false, false
	if n.prefs != nil {
		replyNotify, adminNotify = n.prefs(ctx)
	}

	if adminNotify && n.admin != "" {
		message := mail.NewCommentForAdmin(notice)
		message.To = n.admin
		if !n.queue.Enqueue(message) {
			n.logger.DebugContext(ctx, "admin notification dropped", slog.String("comment_id", c.ID))
		}
	}

	if !replyNotify || notification.Parent == nil {
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
	message := mail.NewReply(notice)
	message.To = to
	if !n.queue.Enqueue(message) {
		n.logger.DebugContext(ctx, "reply notification dropped", slog.String("comment_id", c.ID))
	}
	return nil
}
