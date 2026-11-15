package spam

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Akismet classifies comments with the WordPress.com service.
//
// It needs the client's IP: without it the API refuses the request, which is
// why this checker reports an error rather than a verdict when the transport
// could not supply one. Guessing "ham" there would silently disable the filter.
type Akismet struct {
	key     string
	siteURL string
	client  *http.Client
}

// NewAkismet returns a checker. The key is the blog's Akismet API key, not the
// account password.
func NewAkismet(key, siteURL string, timeout time.Duration) (*Akismet, error) {
	key = strings.TrimSpace(key)
	siteURL = strings.TrimRight(strings.TrimSpace(siteURL), "/")
	if key == "" {
		return nil, fmt.Errorf("akismet: key is required")
	}
	if siteURL == "" {
		return nil, fmt.Errorf("akismet: site URL is required")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Akismet{
		key:     key,
		siteURL: siteURL,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

// Verify checks the key against the site URL. It runs once at startup so a
// mistyped key surfaces next to the configuration that caused it, rather than
// as a warning on the first comment.
func (a *Akismet) Verify(ctx context.Context) error {
	form := url.Values{"key": {a.key}, "blog": {a.siteURL}}
	resp, err := a.post(ctx, "https://rest.akismet.com/1.1/verify-key", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if err != nil {
		return fmt.Errorf("akismet: read verify response: %w", err)
	}
	if strings.TrimSpace(string(body)) != "valid" {
		return fmt.Errorf("akismet: key rejected for %s: %s", a.siteURL, truncateDescription(body, 120))
	}
	return nil
}

// Check asks Akismet whether the comment is spam.
func (a *Akismet) Check(ctx context.Context, c Content) (Verdict, error) {
	if strings.TrimSpace(c.IP) == "" {
		return Verdict{}, fmt.Errorf("%w: akismet needs the client IP", ErrUnavailable)
	}

	form := url.Values{
		"blog":                  {a.siteURL},
		"user_ip":               {c.IP},
		"user_agent":            {trim(c.UserAgent, 255)},
		"referrer":              {trim(c.Referer, 255)},
		"comment_type":          {"comment"},
		"comment_author":        {trim(c.Author, 100)},
		"comment_author_email":  {trim(c.Email, 254)},
		"comment_author_url":    {trim(c.Website, 200)},
		"comment_content":       {trim(c.Body, 4000)},
		"blog_lang":             {"zh"},
		"permalink":             {permalink(c)},
		"comment_post_modified": {""},
	}

	endpoint := fmt.Sprintf("https://%s.rest.akismet.com/1.1/comment-check", a.key)
	resp, err := a.post(ctx, endpoint, form)
	if err != nil {
		return Verdict{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if err != nil {
		return Verdict{}, fmt.Errorf("akismet: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Akismet answers 400 with a plain-text explanation (missing field,
		// invalid key). Surfacing it beats "request failed".
		return Verdict{}, fmt.Errorf("%w: akismet %d: %s",
			ErrUnavailable, resp.StatusCode, truncateDescription(body, 160))
	}

	switch strings.TrimSpace(string(body)) {
	case "true":
		reason := "akismet"
		if resp.Header.Get("X-Akismet-Pro-Tip") == "discard" {
			reason = "akismet (pro-tip: discard)"
		}
		return Verdict{Spam: true, Reason: reason}, nil
	case "false":
		return Verdict{Spam: false, Reason: "akismet"}, nil
	default:
		return Verdict{}, fmt.Errorf("%w: akismet answered %q", ErrUnavailable, truncateDescription(body, 80))
	}
}

func (a *Akismet) post(ctx context.Context, endpoint string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("akismet: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Akismet asks every client to identify itself; a bare Go user agent is
	// indistinguishable from a scraper.
	req.Header.Set("User-Agent", "Astro-Liomix/1.0 | Akismet/1.1")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: akismet: %v", ErrUnavailable, err)
	}
	return resp, nil
}

// permalink is the comment's public address, when one can be built.
func permalink(c Content) string {
	if c.SiteURL == "" {
		return ""
	}
	if c.PostSlug == "" {
		return c.SiteURL + "/"
	}
	return strings.TrimRight(c.SiteURL, "/") + "/posts/" + c.PostSlug
}
