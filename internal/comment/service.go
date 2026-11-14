package comment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/id"
)

// Page size defaults. They are overridable through WithPageSize.
const (
	defaultPageSize = 20
	defaultMaxPage  = 100
)

// Service holds the comment use cases.
type Service struct {
	repo        Repository
	moderator   Moderator
	ids         id.Generator
	now         func() time.Time
	logger      *slog.Logger
	pageSize    int
	maxPageSize int
}

// Option configures a Service. Options are applied in order, so later ones win.
type Option func(*Service)

// WithModerator replaces the default "always pending" review policy.
func WithModerator(m Moderator) Option {
	return func(s *Service) {
		if m != nil {
			s.moderator = m
		}
	}
}

// WithIDGenerator replaces the crypto/rand identifier source, mainly in tests.
func WithIDGenerator(g id.Generator) Option {
	return func(s *Service) {
		if g != nil {
			s.ids = g
		}
	}
}

// WithClock replaces the time source, mainly in tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithLogger sets the structured logger. A nil logger keeps slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithPageSize sets the default and maximum page size. Non-positive values are
// ignored; a maximum below the default is raised to the default so the window
// stays coherent.
func WithPageSize(defaultSize, maxSize int) Option {
	return func(s *Service) {
		if defaultSize > 0 {
			s.pageSize = defaultSize
		}
		switch {
		case maxSize <= 0:
			// Keep the current maximum but never let it sit below the default.
			if s.maxPageSize < s.pageSize {
				s.maxPageSize = s.pageSize
			}
		case maxSize < s.pageSize:
			s.maxPageSize = s.pageSize
		default:
			s.maxPageSize = maxSize
		}
	}
}

// NewService wires a comment service. It fails fast on a missing repository
// rather than panicking on the first request.
func NewService(repo Repository, opts ...Option) (*Service, error) {
	if repo == nil {
		return nil, errors.New("comment: repository is required")
	}
	s := &Service{
		repo:        repo,
		moderator:   pendingReviewer{},
		ids:         id.RandomGenerator{},
		now:         time.Now,
		logger:      slog.Default(),
		pageSize:    defaultPageSize,
		maxPageSize: defaultMaxPage,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// CreateInput is the data a client submits to publish a comment. It is
// deliberately separate from Comment: clients never choose IDs, status or
// timestamps.
type CreateInput struct {
	PostSlug string
	ParentID string
	Author   CommentAuthor
	Body     string
}

// Create validates and stores a new comment.
//
// The comment starts in the status chosen by the configured Moderator (pending
// by default) and is returned as stored, including server-assigned fields.
func (s *Service) Create(ctx context.Context, in CreateInput) (*Comment, error) {
	now := s.now().UTC()
	c := &Comment{
		ID:       s.ids.New(),
		PostSlug: strings.TrimSpace(in.PostSlug),
		ParentID: strings.TrimSpace(in.ParentID),
		Author: CommentAuthor{
			Name:    strings.TrimSpace(in.Author.Name),
			Email:   strings.TrimSpace(in.Author.Email),
			Website: strings.TrimSpace(in.Author.Website),
		},
		Body:      NormalizeBody(in.Body),
		Status:    StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("validate comment: %w", err)
	}
	if c.ParentID != "" {
		if err := s.checkParent(ctx, c); err != nil {
			return nil, err
		}
	}

	status, err := s.moderator.Review(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("review comment: %w", err)
	}
	c.Status = status

	if err := s.repo.Create(ctx, c); err != nil {
		return nil, fmt.Errorf("store comment: %w", err)
	}
	s.logger.InfoContext(ctx, "comment created",
		slog.String("comment_id", c.ID),
		slog.String("post_slug", c.PostSlug),
		slog.String("status", string(c.Status)),
	)
	return c.Clone(), nil
}

// Get returns a single comment by ID.
func (s *Service) Get(ctx context.Context, commentID string) (*Comment, error) {
	c, err := s.repo.Get(ctx, commentID)
	if err != nil {
		return nil, fmt.Errorf("get comment %q: %w", commentID, err)
	}
	return c, nil
}

// List returns comments filtered by q, newest first. Limit is clamped to the
// configured page size so a client cannot ask for an unbounded page.
func (s *Service) List(ctx context.Context, q Query) ([]*Comment, error) {
	if err := q.Validate(); err != nil {
		return nil, fmt.Errorf("validate query: %w", err)
	}
	q.Limit = s.clampLimit(q.Limit)

	comments, err := s.repo.List(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	return comments, nil
}

// PageSize returns the default page size applied to listings.
func (s *Service) PageSize() int { return s.pageSize }

// MaxPageSize returns the hard upper bound applied to listings.
func (s *Service) MaxPageSize() int { return s.maxPageSize }

func (s *Service) clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return s.pageSize
	case limit > s.maxPageSize:
		return s.maxPageSize
	default:
		return limit
	}
}

// checkParent verifies that the parent comment exists and belongs to the same
// post, which keeps threads from spanning unrelated articles.
func (s *Service) checkParent(ctx context.Context, c *Comment) error {
	parent, err := s.repo.Get(ctx, c.ParentID)
	if err != nil {
		return fmt.Errorf("resolve parent comment %q: %w", c.ParentID, err)
	}
	if parent.PostSlug != c.PostSlug {
		return &apperr.ValidationError{
			Field:   "parentId",
			Message: "refers to a comment on a different post",
		}
	}
	return nil
}

// pendingReviewer is the default Moderator: every comment waits for a human to
// approve it.
type pendingReviewer struct{}

// Review implements Moderator.
func (pendingReviewer) Review(context.Context, *Comment) (CommentStatus, error) {
	return StatusPending, nil
}
