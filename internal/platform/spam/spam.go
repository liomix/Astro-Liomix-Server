// Package spam classifies comments with an external service.
//
// It knows nothing about the comment module: a caller hands it the text and a
// little context, and gets back "spam or not". The adapter that maps a
// classification onto a moderation state lives in cmd/server, where the
// comment module is visible.
//
// Every checker here is optional and every failure is the caller's to decide
// about. The convention this package follows is that a filter which cannot
// answer must not answer "ham": the caller keeps such comments pending, which
// is the safe default.
package spam

import (
	"context"
	"errors"
	"strings"
)

// Content is one comment as a filter sees it.
type Content struct {
	Author  string
	Email   string
	Website string
	Body    string
	// PostSlug is the article the comment belongs to. Filters that learn per
	// site use it as context, not as a label.
	PostSlug string
	// SiteURL identifies the site to the filter. Akismet requires it.
	SiteURL string
	// IP, UserAgent and Referer are the request facts a filter uses to spot
	// bots. They are not stored by this service; they only travel here.
	IP        string
	UserAgent string
	Referer   string
}

// Verdict is one filter's answer.
type Verdict struct {
	// Spam is true when the content should be hidden without a human reading
	// it. A false value means "not spam", never "approved": moderation is still
	// the site owner's decision.
	Spam bool
	// Reason is a short human-readable note recorded in the log. It is not
	// shown to commenters.
	Reason string
}

// Checker classifies a comment.
type Checker interface {
	Check(ctx context.Context, c Content) (Verdict, error)
}

// ErrUnavailable means the filter could not be reached or refused to answer.
// Callers treat it as "no verdict" and leave the comment pending.
var ErrUnavailable = errors.New("spam: filter unavailable")

// trim shortens a field to the length filters accept, without cutting a
// multi-byte character in half.
func trim(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// truncateDescription keeps log lines bounded when a provider answers with a
// page of HTML instead of a verdict.
func truncateDescription(body []byte, limit int) string {
	text := strings.TrimSpace(string(body))
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
