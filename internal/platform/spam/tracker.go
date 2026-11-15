package spam

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// stats is what the history knows about this submission, after counting it.
//
// Counts include the submission being judged, so an ipCount of 6 with a limit
// of 5 means "this is the sixth from that address in the window".
type stats struct {
	ipCount    int
	emailCount int
	// duplicates is how many identical bodies were already seen in the window.
	duplicates int
}

// tracker remembers recent submissions per address, per email and per body.
//
// It is in memory on purpose: a spam counter that survives a restart is a table
// an attacker can also fill, and losing the window on deploy costs at most one
// duplicate comment. Every access is bounded — see MaxTracked.
type tracker struct {
	cfg Config
	now func() time.Time

	mu     sync.Mutex
	ips    map[string]*bucket
	emails map[string]*bucket
	bodies map[string]*bucket
	swept  time.Time
}

// bucket holds the timestamps of recent hits for one key, oldest first.
type bucket struct{ at []time.Time }

func newTracker(cfg Config, now func() time.Time) *tracker {
	return &tracker{
		cfg:    cfg,
		now:    now,
		ips:    make(map[string]*bucket),
		emails: make(map[string]*bucket),
		bodies: make(map[string]*bucket),
	}
}

// observe records this submission and reports the counts the rules need.
func (t *tracker) observe(ip, email, body string) stats {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	// The address counts against the flood window, everything else against the
	// softer one: ten comments in thirty seconds has no innocent explanation, a
	// fourth comment in ten minutes does.
	floodCutoff := now.Add(-t.cfg.FloodWindow)
	cutoff := now.Add(-t.cfg.Window)

	var out stats
	if ip != "" {
		out.ipCount = t.bump(t.ips, ip, now, floodCutoff)
	}
	if email != "" {
		out.emailCount = t.bump(t.emails, email, now, cutoff)
	}
	if body != "" {
		// One means this body is new; anything above that counts the earlier
		// copies.
		if seen := t.bump(t.bodies, body, now, cutoff); seen > 1 {
			out.duplicates = seen - 1
		}
	}
	t.sweep(now, cutoff, floodCutoff)
	return out
}

// bump prunes one key's history and appends the current time.
func (t *tracker) bump(m map[string]*bucket, key string, now, cutoff time.Time) int {
	b, ok := m[key]
	if !ok {
		b = &bucket{}
		m[key] = b
	}
	b.at = dropBefore(b.at, cutoff)
	b.at = append(b.at, now)
	return len(b.at)
}

// sweep drops keys that have gone quiet. It runs at most once a minute so a
// burst of comments does not pay for a full map walk on every one of them.
func (t *tracker) sweep(now, cutoff, floodCutoff time.Time) {
	if now.Sub(t.swept) < time.Minute && len(t.ips)+len(t.emails)+len(t.bodies) <= t.cfg.MaxTracked {
		return
	}
	t.swept = now
	t.pruneAll(t.ips, floodCutoff)
	t.pruneAll(t.emails, cutoff)
	t.pruneAll(t.bodies, cutoff)

	// A flood can create keys faster than the window expires them. Dropping the
	// history loses accuracy for a few minutes; growing without a bound loses
	// the process.
	if len(t.ips)+len(t.emails)+len(t.bodies) > t.cfg.MaxTracked {
		t.ips = make(map[string]*bucket)
		t.emails = make(map[string]*bucket)
		t.bodies = make(map[string]*bucket)
	}
}

func (t *tracker) pruneAll(m map[string]*bucket, cutoff time.Time) {
	for key, b := range m {
		b.at = dropBefore(b.at, cutoff)
		if len(b.at) == 0 {
			delete(m, key)
		}
	}
}

// dropBefore returns the part of a sorted slice that is newer than cutoff.
func dropBefore(at []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(at) && !at[i].After(cutoff) {
		i++
	}
	if i == 0 {
		return at
	}
	return at[i:]
}

// Link extraction lives here because the tracker and the rules both need the
// normalized body it works on.
var linkPattern = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>"'\x60]+`)

// countLinks returns how many addresses the text carries.
func countLinks(text string) int {
	return len(linkPattern.FindAllString(text, -1))
}

// linkHosts returns the host of every address in the text, normalized.
func linkHosts(text string) []string {
	raw := linkPattern.FindAllString(text, -1)
	hosts := make([]string, 0, len(raw))
	for _, link := range raw {
		link = strings.TrimSuffix(strings.TrimSuffix(link, "."), ")")
		if strings.HasPrefix(strings.ToLower(link), "www.") {
			link = "http://" + link
		}
		host := hostOf(link)
		if host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// hostOf keeps the host of a URL without pulling in net/url's error handling
// for what is, after the regexp above, a trusted shape.
func hostOf(link string) string {
	rest := link
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	// A port is not part of the domain a blocklist knows.
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		rest = rest[:i]
	}
	return strings.ToLower(strings.TrimSpace(rest))
}
