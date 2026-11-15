package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/comment"
)

// stubModerator returns a fixed status, or an error.
type stubModerator struct {
	status comment.CommentStatus
	err    error
}

func (s stubModerator) Review(context.Context, *comment.Comment, comment.RequestMeta) (comment.CommentStatus, error) {
	return s.status, s.err
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func policyWith(spam comment.Moderator, auto bool, policyErr error) reviewPolicy {
	return reviewPolicy{
		spam: spam,
		autoApprove: func(context.Context) (bool, error) {
			return auto, policyErr
		},
		logger: quietLogger(),
	}
}

func TestReviewPolicyDefaultsToPending(t *testing.T) {
	t.Parallel()

	status, err := policyWith(nil, false, nil).Review(t.Context(), &comment.Comment{}, comment.RequestMeta{})
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if status != comment.StatusPending {
		t.Errorf("status = %q, want pending by default", status)
	}
}

func TestReviewPolicyAutoApprove(t *testing.T) {
	t.Parallel()

	status, err := policyWith(nil, true, nil).Review(t.Context(), &comment.Comment{}, comment.RequestMeta{})
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if status != comment.StatusApproved {
		t.Errorf("status = %q, want approved with the switch on", status)
	}
}

// Spam is asked first: "no review needed" is about the operator's time, not
// permission to publish adverts.
func TestReviewPolicySpamWinsOverAutoApprove(t *testing.T) {
	t.Parallel()

	spam := stubModerator{status: comment.StatusSpam}
	status, err := policyWith(spam, true, nil).Review(t.Context(), &comment.Comment{}, comment.RequestMeta{})
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if status != comment.StatusSpam {
		t.Errorf("status = %q, want spam even with auto-approve on", status)
	}
}

// A filter that cannot answer must not stop the comment, and must not be read as
// "not spam" either: the policy still decides.
func TestReviewPolicySurvivesFilterFailure(t *testing.T) {
	t.Parallel()

	failing := stubModerator{err: errors.New("filter exploded")}
	status, err := policyWith(failing, false, nil).Review(t.Context(), &comment.Comment{}, comment.RequestMeta{})
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if status != comment.StatusPending {
		t.Errorf("status = %q, want pending when the filter failed and auto-approve is off", status)
	}

	status, err = policyWith(failing, true, nil).Review(t.Context(), &comment.Comment{}, comment.RequestMeta{})
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if status != comment.StatusApproved {
		t.Errorf("status = %q, want approved — the failure must not veto the switch", status)
	}
}

// Losing the switch means "ask a human".
func TestReviewPolicyReadFailureKeepsPending(t *testing.T) {
	t.Parallel()

	status, err := policyWith(nil, true, errors.New("settings unavailable")).
		Review(t.Context(), &comment.Comment{}, comment.RequestMeta{})
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if status != comment.StatusPending {
		t.Errorf("status = %q, want pending when the policy cannot be read", status)
	}
}
