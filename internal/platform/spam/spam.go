// Package spam scores a comment against a set of local rules.
//
// There is no external service and no network here: every rule is a pure
// function of the comment, the request metadata and a small in-memory history,
// which makes the verdict reproducible, testable without a key, and free to run
// on every submission.
//
// The output is a verdict plus the evidence behind it. Rules are split into hard
// (contact details, duplicate body, one client flooding — each decisive alone)
// and soft (links, word hits, capitals — each only meaningful in company). The
// split matters more than the score: adding soft rules must not make false
// positives more likely, and a sum alone cannot promise that.
package spam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// Content is one comment as the filter sees it.
//
// IP, UserAgent and Referer are request facts the service deliberately does not
// store; they travel here and go no further.
type Content struct {
	Author    string
	Email     string
	Website   string
	Body      string
	PostSlug  string
	IP        string
	UserAgent string
	Referer   string
}

// Verdict is the filter's answer.
type Verdict struct {
	// Spam is true when a hard rule fired, or when the soft score reached the
	// threshold with at least two kinds of evidence behind it.
	Spam bool
	// Score is the sum of every rule that fired.
	Score int
	// Reasons names the rules that fired, in the order they ran. They go to the
	// server log — never to the commenter, who should not be taught the rules.
	Reasons []string
}

// Checker classifies a comment. It exists so the adapter in cmd/server depends
// on "something that judges", not on this package's internals.
type Checker interface {
	Check(ctx context.Context, c Content) (Verdict, error)
}

// Default tuning. The threshold is deliberately high enough that one or two
// rule hits (a link, an exclamation mark) never hide a comment on their own.
// minSoftCategories is how many different kinds of soft evidence are needed
// before the threshold decides anything. Two weak signals of the same kind —
// three links, two keyword groups — are one observation repeated, not two.
const minSoftCategories = 2

const (
	defaultThreshold   = 60
	defaultWindow      = 10 * time.Minute
	defaultMaxPerEmail = 3
	defaultMaxTracked  = 20000

	// The flood gate is deliberately narrow and brutal: ten comments from one
	// address in half a minute is not a person reading, it is a script, and no
	// combination of soft evidence should be needed to say so.
	defaultFloodCount  = 10
	defaultFloodWindow = 30 * time.Second
)

// Config is the filter's tuning. The zero value is not usable — use
// DefaultConfig — because a zero threshold would mark every comment as spam.
type Config struct {
	// Threshold is the soft score at which a comment is stored as spam. It only
	// decides together with minSoftCategories, so a single heavy signal — a long
	// word-list hit, a giant body — cannot block on its own.
	Threshold int
	// Window is the period the soft frequency rule counts over.
	Window time.Duration
	// MaxPerEmail is how many comments one email may post within Window before
	// that starts scoring. It stays soft: a household can share an address.
	MaxPerEmail int
	// FloodCount and FloodWindow are the instant-kill gate: FloodCount comments
	// from one address inside FloodWindow and the comment is spam outright.
	// Short window on purpose — a slow trickle is what the soft rules are for.
	FloodCount  int
	FloodWindow time.Duration
	// MaxTracked bounds the in-memory history. Reaching it drops the history
	// rather than growing without limit: an attacker who floods hard enough
	// costs the filter its accuracy, never the process its memory.
	MaxTracked int
}

// DefaultConfig returns the shipped tuning.
func DefaultConfig() Config {
	return Config{
		Threshold:   defaultThreshold,
		Window:      defaultWindow,
		MaxPerEmail: defaultMaxPerEmail,
		FloodCount:  defaultFloodCount,
		FloodWindow: defaultFloodWindow,
		MaxTracked:  defaultMaxTracked,
	}
}

// Validate reports the first value that cannot work.
func (c Config) Validate() error {
	if c.Threshold <= 0 {
		return &apperr.ValidationError{Field: "threshold", Code: apperr.FieldOutOfRange, Message: "must be positive"}
	}
	if c.Window <= 0 {
		return &apperr.ValidationError{Field: "window", Code: apperr.FieldOutOfRange, Message: "must be positive"}
	}
	if c.MaxPerEmail <= 0 {
		return &apperr.ValidationError{Field: "maxPerEmail", Code: apperr.FieldOutOfRange, Message: "must be positive"}
	}
	if c.FloodCount <= 0 || c.FloodWindow <= 0 {
		return &apperr.ValidationError{Field: "flood", Code: apperr.FieldOutOfRange, Message: "must be positive"}
	}
	if c.MaxTracked <= 0 {
		return &apperr.ValidationError{Field: "maxTracked", Code: apperr.FieldOutOfRange, Message: "must be positive"}
	}
	return nil
}

// Filter is the rule engine. It is safe for concurrent use.
type Filter struct {
	cfg     Config
	now     func() time.Time
	lexicon []Group
	seen    *tracker
}

var _ Checker = (*Filter)(nil)

// Option configures a Filter. Options apply in order, later ones winning.
type Option func(*Filter)

// WithConfig replaces the default tuning. An invalid config falls back to the
// defaults rather than failing construction: a typo in a threshold must not take
// comment submission down with it.
func WithConfig(cfg Config) Option {
	return func(f *Filter) {
		if cfg.Validate() == nil {
			f.cfg = cfg
		}
	}
}

// WithLexicon replaces the word list. An empty list keeps the embedded default:
// a filter with no terms would silently stop catching adverts.
func WithLexicon(groups []Group) Option {
	return func(f *Filter) {
		if len(groups) > 0 {
			f.lexicon = groups
		}
	}
}

// WithClock replaces the time source, mainly in tests.
func WithClock(now func() time.Time) Option {
	return func(f *Filter) {
		if now != nil {
			f.now = now
		}
	}
}

// New returns a filter with the defaults applied.
func New(opts ...Option) *Filter {
	f := &Filter{cfg: DefaultConfig(), now: time.Now, lexicon: defaultLexicon}
	for _, opt := range opts {
		opt(f)
	}
	f.seen = newTracker(f.cfg, f.now)
	return f
}

// Check scores one comment.
//
// It never returns an error for a comment it dislikes: "spam" is an answer, not
// a failure. The error is reserved for a cancelled context, which the adapter
// turns into "keep it pending" without pretending it was judged.
func (f *Filter) Check(ctx context.Context, c Content) (Verdict, error) {
	if err := ctx.Err(); err != nil {
		return Verdict{}, err
	}

	// Folding full-width characters and dropping zero-width ones happens before
	// anything looks at the text, so every rule sees what a reader would.
	c.Body = normalize(c.Body)
	c.Author = normalize(c.Author)

	seen := f.seen.observe(c.IP, strings.ToLower(strings.TrimSpace(c.Email)), fingerprint(c.Body))

	score := 0
	blocked := false
	reasons := make([]string, 0, 4)
	categories := make(map[string]struct{}, 4)
	for _, rule := range rules {
		for _, hit := range rule(c, seen, f.cfg, f.lexicon) {
			score += hit.points
			reasons = append(reasons, hit.reason)
			if hit.kind == hard {
				blocked = true
				continue
			}
			categories[hit.category] = struct{}{}
		}
	}

	// A hard hit stands on its own. Soft evidence only decides when it is both
	// heavy enough and varied enough; that second condition is what keeps the
	// false-positive rate from creeping up every time a rule is added.
	if !blocked && score >= f.cfg.Threshold && len(categories) >= minSoftCategories {
		blocked = true
	}

	return Verdict{Spam: blocked, Score: score, Reasons: reasons}, nil
}

// fingerprint collapses a body to a value two copies share: lower-cased, every
// run of whitespace flattened. Two comments differing only in spacing are the
// same comment, and that is exactly what the duplicate rule looks for.
func fingerprint(body string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(body)), " ")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:16])
}
