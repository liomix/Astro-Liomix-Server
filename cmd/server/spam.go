package main

import (
	"context"
	"log/slog"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/spam"
)

// spamModerator adapts an external spam filter to the comment module's review
// port.
//
// It can only ever make a comment *less* visible: a spam verdict stores the
// comment as spam, and anything else leaves it pending. Auto-approving on a
// "not spam" answer would turn a rule table into the publisher of this blog,
// which is not a decision a filter gets to make.
//
// A filter that fails is not a filter that said "fine": the error is logged and
// the comment stays pending, so an outage degrades to the behaviour the site had
// before the filter existed.
type spamModerator struct {
	checker spam.Checker
	logger  *slog.Logger
}

var _ comment.Moderator = spamModerator{}

// Review implements comment.Moderator.
func (m spamModerator) Review(ctx context.Context, c *comment.Comment, meta comment.RequestMeta) (comment.CommentStatus, error) {
	verdict, err := m.checker.Check(ctx, spam.Content{
		Author:    c.Author.Name,
		Email:     c.Author.Email,
		Website:   c.Author.Website,
		Body:      c.Body,
		PostSlug:  c.PostSlug,
		IP:        meta.IP,
		UserAgent: meta.UserAgent,
		Referer:   meta.Referer,
	})
	if err != nil {
		m.logger.WarnContext(ctx, "spam filter did not answer, keeping the comment pending",
			slog.String("comment_id", c.ID),
			slog.Any("error", err))
		return comment.StatusPending, nil
	}
	if !verdict.Spam {
		return comment.StatusPending, nil
	}

	m.logger.InfoContext(ctx, "spam filter rejected a comment",
		slog.String("comment_id", c.ID),
		slog.String("post_slug", c.PostSlug),
		slog.Int("score", verdict.Score),
		slog.Any("reasons", verdict.Reasons))
	return comment.StatusSpam, nil
}
