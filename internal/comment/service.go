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

// MaxBulkIDs caps one batch operation. It matches one console page: an operator
// selects what they can see, and a larger batch would be a script driven
// through a UI.
const MaxBulkIDs = 100

// Service holds the comment use cases.
type Service struct {
	repo        Repository
	moderator   Moderator
	notifier    Notifier
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

// WithNotifier installs the notification sink. Without one, approved comments
// simply do not notify anyone — the right default for tests and for a
// deployment that has no mail server.
func WithNotifier(n Notifier) Option {
	return func(s *Service) {
		s.notifier = n
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
	// Meta is the request context a spam filter needs. It is never stored.
	Meta RequestMeta
}

// Create validates and stores a new comment.
//
// The comment starts in the status chosen by the configured Moderator (pending
// by default) and is returned as stored, including server-assigned fields.
//
// It issues no delete credential: callers that do (the HTTP transport, so the
// author can take their comment back) use CreateWithCredential. Imports and
// tests take this path on purpose — an imported row has no author to hand a
// secret to, and minting one per row would only create credentials nobody can
// use.
func (s *Service) Create(ctx context.Context, in CreateInput) (*Comment, error) {
	c, err := s.prepareComment(ctx, in)
	if err != nil {
		return nil, err
	}
	return s.persist(ctx, c)
}

// CreateWithCredential is Create plus the author's deletion credential. The
// token is returned in plaintext exactly once and is stored only as a digest,
// so the caller must hand it to the author immediately or lose it.
func (s *Service) CreateWithCredential(ctx context.Context, in CreateInput) (*Comment, string, error) {
	token, err := newDeleteToken()
	if err != nil {
		return nil, "", err
	}

	c, err := s.prepareComment(ctx, in)
	if err != nil {
		return nil, "", err
	}
	c.DeleteTokenHash = hashDeleteToken(token)

	saved, err := s.persist(ctx, c)
	if err != nil {
		return nil, "", err
	}
	return saved, token, nil
}

// persist stores a prepared comment and logs the outcome.
func (s *Service) persist(ctx context.Context, c *Comment) (*Comment, error) {
	if err := s.repo.Create(ctx, c); err != nil {
		return nil, fmt.Errorf("store comment: %w", err)
	}
	s.logger.InfoContext(ctx, "comment created",
		slog.String("comment_id", c.ID),
		slog.String("post_slug", c.PostSlug),
		slog.String("status", string(c.Status)),
	)
	s.notifyApproved(ctx, c)
	return c.Clone(), nil
}

// store builds and reviews a comment without touching the repository. It is
// the shared front half of both create paths.
func (s *Service) prepareComment(ctx context.Context, in CreateInput) (*Comment, error) {
	now := s.timestamp()
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

	status, err := s.moderator.Review(ctx, c, in.Meta)
	if err != nil {
		return nil, fmt.Errorf("review comment: %w", err)
	}
	c.Status = status
	return c, nil
}

// Get returns a single comment by ID.
func (s *Service) Get(ctx context.Context, commentID string) (*Comment, error) {
	c, err := s.repo.Get(ctx, commentID)
	if err != nil {
		return nil, fmt.Errorf("get comment %q: %w", commentID, err)
	}
	return c, nil
}

// List returns one page of comments, newest first, plus the cursor that
// continues after it.
//
// The limit is clamped to the configured maximum so a client cannot ask for an
// unbounded page. The next cursor is empty exactly when the page is the last
// one, which the service decides by asking the repository for one row more than
// the page holds: a second COUNT query would cost more than the page itself on
// a long thread, and would disagree with it anyway if a comment arrived in
// between.
func (s *Service) List(ctx context.Context, q Query) ([]*Comment, string, error) {
	if err := q.Validate(); err != nil {
		return nil, "", fmt.Errorf("validate query: %w", err)
	}
	q.Limit = s.clampLimit(q.Limit)

	page := q
	page.Limit = q.Limit + 1

	comments, err := s.repo.List(ctx, page)
	if err != nil {
		return nil, "", fmt.Errorf("list comments: %w", err)
	}
	if len(comments) <= q.Limit {
		return comments, "", nil
	}

	comments = comments[:q.Limit]
	last := comments[len(comments)-1]
	return comments, EncodeCursor(Cursor{CreatedAt: last.CreatedAt, ID: last.ID}), nil
}

// CountByPost reports how many comments each requested post has.
//
// Slugs are cleaned exactly like a listing request (trim, drop blanks,
// deduplicate, keep the caller's order, cap the batch), and every slug that
// survived cleaning appears in the result — posts without a single comment
// included, with a count of zero. A caller rendering a list of cards needs to
// tell "no comments yet" from "we did not ask", and a missing key cannot.
//
// Only approved comments are counted. A pending comment is not part of what
// readers see, and a card promising three comments above a thread showing two
// reads as a bug; undercounting until moderation catches up does not.
func (s *Service) CountByPost(ctx context.Context, slugs []string) ([]PostCount, error) {
	cleaned, err := cleanSlugs(slugs)
	if err != nil {
		return nil, fmt.Errorf("count comments: %w", err)
	}

	q := CountQuery{PostSlugs: cleaned, Status: StatusApproved}
	if err := q.Validate(); err != nil {
		return nil, fmt.Errorf("validate query: %w", err)
	}

	counts, err := s.repo.CountByPost(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("count comments: %w", err)
	}

	// Rebuild in request order: the adapter answers with a map, and a map has no
	// order to preserve.
	out := make([]PostCount, 0, len(cleaned))
	for _, slug := range cleaned {
		out = append(out, PostCount{Slug: slug, Count: counts[slug]})
	}
	return out, nil
}

// CountByStatus reports how many comments sit in each moderation state.
//
// Every known state is present in the answer, zero included, so a console can
// render four tabs without deciding what a missing key means. The status field
// of q is ignored by design: asking "how many, by state" is one question, not
// four.
func (s *Service) CountByStatus(ctx context.Context, q Query) (map[CommentStatus]int, error) {
	q.Status = ""
	q.Keyword = strings.TrimSpace(q.Keyword)
	q.Limit = 0
	q.Cursor = nil
	if err := q.Validate(); err != nil {
		return nil, fmt.Errorf("validate query: %w", err)
	}

	counts, err := s.repo.CountByStatus(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("count comments by status: %w", err)
	}

	out := make(map[CommentStatus]int, 4)
	for _, status := range []CommentStatus{StatusPending, StatusApproved, StatusRejected, StatusSpam} {
		out[status] = counts[status]
	}
	return out, nil
}

// CountSince reports how many comments arrived at or after since.
func (s *Service) CountSince(ctx context.Context, since time.Time) (int, error) {
	total, err := s.repo.CountSince(ctx, since)
	if err != nil {
		return 0, fmt.Errorf("count comments: %w", err)
	}
	return total, nil
}

// cleanSlugs trims, validates, deduplicates and caps a slug batch, preserving
// the caller's order. Duplicates are dropped rather than rejected: a client
// assembling the list from page content can easily name the same post twice,
// and the answer would be the same either way.
func cleanSlugs(slugs []string) ([]string, error) {
	seen := make(map[string]struct{}, len(slugs))
	cleaned := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		if err := ValidatePostSlug(slug); err != nil {
			return nil, err
		}
		seen[slug] = struct{}{}
		cleaned = append(cleaned, slug)
	}
	if len(cleaned) == 0 {
		return nil, &apperr.ValidationError{Field: "slugs", Message: "must name at least one post"}
	}
	if len(cleaned) > MaxCountSlugs {
		return nil, &apperr.ValidationError{
			Field:   "slugs",
			Message: fmt.Sprintf("must not name more than %d posts", MaxCountSlugs),
		}
	}
	return cleaned, nil
}

// SetStatus moves a comment to another moderation state.
//
// Approving is the only transition that makes a comment publicly readable, and
// the service is where it is validated: the transport must not be able to store
// a status the rest of the system does not know.
func (s *Service) SetStatus(ctx context.Context, commentID string, status CommentStatus) (*Comment, error) {
	commentID = strings.TrimSpace(commentID)
	if commentID == "" {
		return nil, &apperr.ValidationError{Field: "id", Message: "is required"}
	}
	if !status.Valid() {
		return nil, &apperr.ValidationError{Field: "status", Message: "must be one of pending, approved, rejected, spam"}
	}

	// The previous state decides whether this is a new approval: re-approving
	// an already approved comment must not send its author another email. A
	// failed read is not fatal — the update below is the operation that
	// matters, and it reports its own errors.
	previous, err := s.repo.Get(ctx, commentID)
	if err != nil {
		s.logger.DebugContext(ctx, "read comment before status change",
			slog.String("comment_id", commentID), slog.Any("error", err))
		previous = nil
	}

	updated, err := s.repo.UpdateStatus(ctx, commentID, status, s.timestamp())
	if err != nil {
		return nil, fmt.Errorf("update comment %q: %w", commentID, err)
	}
	s.logger.InfoContext(ctx, "comment status changed",
		slog.String("comment_id", updated.ID),
		slog.String("post_slug", updated.PostSlug),
		slog.String("status", string(updated.Status)),
	)
	if previous == nil || previous.Status != StatusApproved {
		s.notifyApproved(ctx, updated)
	}
	return updated, nil
}

// SetStatusMany applies one moderation decision to a batch.
//
// Notifications are sent only for comments that became approved, and a failure
// to notify never fails the batch: the rows are already stored.
func (s *Service) SetStatusMany(ctx context.Context, commentIDs []string, status CommentStatus) (int, error) {
	if !status.Valid() {
		return 0, &apperr.ValidationError{Field: "status", Message: "must be one of " + statusList}
	}
	ids, err := cleanIDs(commentIDs)
	if err != nil {
		return 0, err
	}

	// The previous state decides who gets mail: re-approving a comment that was
	// already visible must not send its author another email. The batch
	// statement returns a count rather than rows, so the rows are read first —
	// capped at MaxBulkIDs for exactly this reason.
	previous := make(map[string]*Comment, len(ids))
	for _, id := range ids {
		c, err := s.repo.Get(ctx, id)
		if err != nil {
			// A row deleted since the page was rendered is normal; the batch
			// below simply will not touch it.
			continue
		}
		previous[id] = c
	}

	affected, err := s.repo.UpdateStatusMany(ctx, ids, status, s.timestamp())
	if err != nil {
		return 0, fmt.Errorf("update %d comments: %w", len(ids), err)
	}
	s.logger.InfoContext(ctx, "comments status changed in bulk",
		slog.Int("requested", len(ids)),
		slog.Int("affected", affected),
		slog.String("status", string(status)),
	)

	if status == StatusApproved {
		for _, id := range ids {
			c, ok := previous[id]
			if !ok || c.Status == StatusApproved {
				continue
			}
			c.Status = StatusApproved
			s.notifyApproved(ctx, c)
		}
	}
	return affected, nil
}

// DeleteMany removes a batch of comments, each with its replies.
func (s *Service) DeleteMany(ctx context.Context, commentIDs []string) (int, error) {
	ids, err := cleanIDs(commentIDs)
	if err != nil {
		return 0, err
	}
	affected, err := s.repo.DeleteMany(ctx, ids)
	if err != nil {
		return 0, fmt.Errorf("delete %d comments: %w", len(ids), err)
	}
	s.logger.InfoContext(ctx, "comments deleted in bulk",
		slog.Int("requested", len(ids)), slog.Int("affected", affected))
	return affected, nil
}

// cleanIDs trims, validates, deduplicates and caps a batch of comment IDs.
func cleanIDs(commentIDs []string) ([]string, error) {
	seen := make(map[string]struct{}, len(commentIDs))
	cleaned := make([]string, 0, len(commentIDs))
	for _, raw := range commentIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		cleaned = append(cleaned, id)
	}
	if len(cleaned) == 0 {
		return nil, &apperr.ValidationError{Field: "ids", Message: "must name at least one comment"}
	}
	if len(cleaned) > MaxBulkIDs {
		return nil, &apperr.ValidationError{Field: "ids",
			Message: fmt.Sprintf("must not name more than %d comments", MaxBulkIDs)}
	}
	return cleaned, nil
}

// notifyApproved tells the notifier about a comment that just became visible.
//
// It never returns an error and never fails the request: the comment is already
// stored, and a mail server being down is not a reason to pretend otherwise.
func (s *Service) notifyApproved(ctx context.Context, c *Comment) {
	if s.notifier == nil || c == nil || c.Status != StatusApproved {
		return
	}

	notification := Notification{Comment: c}
	if c.ParentID != "" {
		parent, err := s.repo.Get(ctx, c.ParentID)
		if err != nil {
			// Without the parent there is nobody to reply-notify; the admin
			// notice can still go out.
			s.logger.WarnContext(ctx, "resolve parent for notification",
				slog.String("comment_id", c.ID), slog.Any("error", err))
		} else {
			notification.Parent = parent
		}
	}
	if err := s.notifier.Notify(ctx, notification); err != nil {
		s.logger.WarnContext(ctx, "notify about approved comment",
			slog.String("comment_id", c.ID), slog.Any("error", err))
	}
}

// Delete removes a comment and every reply beneath it.
//
// The cascade lives in the repository so a SQL adapter can express it as one
// statement instead of a service-side loop that reads every row. This is the
// owner-of-the-site path: it assumes the caller was already authorized.
func (s *Service) Delete(ctx context.Context, commentID string) error {
	commentID = strings.TrimSpace(commentID)
	if commentID == "" {
		return &apperr.ValidationError{Field: "id", Message: "is required"}
	}
	if err := s.repo.Delete(ctx, commentID); err != nil {
		return fmt.Errorf("delete comment %q: %w", commentID, err)
	}
	s.logger.InfoContext(ctx, "comment deleted", slog.String("comment_id", commentID))
	return nil
}

// DeleteOwned removes a comment on behalf of its author, proving the claim with
// the credential issued at creation time.
//
// The token is checked against the stored digest before the row is touched, so
// a wrong guess cannot delete anything and cannot tell an existing comment from
// a missing one by the number of round trips. A comment created without a
// credential (imports, or a row written before credentials existed) is never
// deletable this way.
func (s *Service) DeleteOwned(ctx context.Context, commentID, token string) error {
	commentID = strings.TrimSpace(commentID)
	if commentID == "" {
		return &apperr.ValidationError{Field: "id", Message: "is required"}
	}

	c, err := s.repo.Get(ctx, commentID)
	if err != nil {
		return fmt.Errorf("delete comment %q: %w", commentID, err)
	}
	if !deleteTokenMatches(c.DeleteTokenHash, token) {
		return fmt.Errorf("delete comment %q: %w", commentID, apperr.ErrUnauthorized)
	}

	if err := s.repo.Delete(ctx, commentID); err != nil {
		return fmt.Errorf("delete comment %q: %w", commentID, err)
	}
	s.logger.InfoContext(ctx, "comment deleted by author", slog.String("comment_id", commentID))
	return nil
}

// timestamp is the clock reading trimmed to the precision storage keeps.
//
// timestamptz holds microseconds, so a value with nanoseconds looks the same
// until it is read back — and then it is not. A client that compares the
// createdAt of a POST response with the one from a later GET would see two
// different strings for one comment. Trimming at the only place that mints
// times keeps the two answers identical.
func (s *Service) timestamp() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
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
	_, err := resolveParent(ctx, s.repo, c)
	return err
}

// resolveParent returns the comment c replies to.
//
// It lives outside Service so that any caller writing a comment directly — an
// import, a repair script — applies the same rule: a thread the API would
// reject must not be creatable by another path either.
func resolveParent(ctx context.Context, repo Repository, c *Comment) (*Comment, error) {
	parent, err := repo.Get(ctx, c.ParentID)
	if err != nil {
		return nil, fmt.Errorf("resolve parent comment %q: %w", c.ParentID, err)
	}
	if parent.PostSlug != c.PostSlug {
		return nil, &apperr.ValidationError{
			Field:   "parentId",
			Message: "refers to a comment on a different post",
		}
	}
	return parent, nil
}

// pendingReviewer is the default Moderator: every comment waits for a human to
// approve it.
type pendingReviewer struct{}

// Review implements Moderator.
func (pendingReviewer) Review(context.Context, *Comment, RequestMeta) (CommentStatus, error) {
	return StatusPending, nil
}
