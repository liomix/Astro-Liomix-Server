package main

import (
	"context"
	"log/slog"

	"github.com/liomix/astro-liomix-server/internal/comment"
)

// reviewPolicy decides the initial state of a new comment.
//
// Two decisions compose, in this order:
//
//  1. the spam filter, which can only ever make a comment less visible;
//  2. the console's auto-approve switch, which removes the human step.
//
// Spam is asked first on purpose. "No review needed" is a statement about the
// operator's time, not permission to publish adverts: turning auto-approve on
// must not turn the filter off.
type reviewPolicy struct {
	// spam is nil when the filter is disabled in the configuration.
	spam comment.Moderator
	// autoApprove reads the switch. It is read per comment rather than captured,
	// so flipping it in the console applies to the next comment, not the next
	// deploy. A nil reader means "off".
	autoApprove func(ctx context.Context) (bool, error)
	logger      *slog.Logger
}

var _ comment.Moderator = reviewPolicy{}

// Review implements comment.Moderator.
func (p reviewPolicy) Review(ctx context.Context, c *comment.Comment, meta comment.RequestMeta) (comment.CommentStatus, error) {
	if p.spam != nil {
		status, err := p.spam.Review(ctx, c, meta)
		if err != nil {
			// A filter that could not answer is not a filter that said "fine",
			// but it must not stop the comment either: fall through and let the
			// policy decide, exactly as if there were no filter.
			p.logger.WarnContext(ctx, "spam filter did not answer", slog.Any("error", err))
		} else if status == comment.StatusSpam {
			return comment.StatusSpam, nil
		}
	}

	auto := false
	if p.autoApprove != nil {
		value, err := p.autoApprove(ctx)
		if err != nil {
			// Losing the switch means "ask a human", which is the safe side.
			p.logger.WarnContext(ctx, "read comment policy, keeping the comment pending", slog.Any("error", err))
			return comment.StatusPending, nil
		}
		auto = value
	}
	if auto {
		return comment.StatusApproved, nil
	}
	return comment.StatusPending, nil
}
