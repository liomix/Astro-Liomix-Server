package spam

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// maxBodyRunes is where a comment stops looking like a comment and starts
// looking like a dumped page. The API rejects far longer bodies already; this is
// the point below that limit where length itself is a signal.
const maxBodyRunes = 6000

// Group is one category of terms from anti-spam-1.txt: a label for the log, the
// score one hit adds, and the words themselves.
type Group struct {
	Label  string
	Points int
	Words  []string
}

// defaultLexiconFile is the shipped word list, embedded so a single binary works
// with no files beside it. SPAM_LEXICON_FILE overrides it at runtime.
//
//go:embed anti-spam-1.txt
var defaultLexiconFile string

// defaultLexicon is parsed once. The file is in this repository, so a broken one
// fails the build's tests rather than a deployment.
var defaultLexicon = mustParseLexicon(defaultLexiconFile)

// DefaultLexicon returns the terms compiled into the binary.
func DefaultLexicon() []Group { return defaultLexicon }

// ParseLexicon reads the word list format: comment lines start with '#', a
// group header is "[label points]", and every other non-empty line is a term.
//
// The format is deliberately dull. This is a file a human edits in a hurry, and
// a term silently landing in the wrong group is worse than a parse error.
func ParseLexicon(text string) ([]Group, error) {
	var (
		groups []Group
		label  string
		points int
		index  = -1
	)
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "["):
			group, err := parseGroupHeader(line)
			if err != nil {
				return nil, fmt.Errorf("anti-spam line %d: %w", n+1, err)
			}
			groups = append(groups, group)
			index = len(groups) - 1
			label, points = group.Label, group.Points
		default:
			if index < 0 {
				return nil, fmt.Errorf("anti-spam line %d: %q appears before any [group]", n+1, line)
			}
			_ = label
			_ = points
			groups[index].Words = append(groups[index].Words, strings.ToLower(line))
		}
	}
	for _, group := range groups {
		if len(group.Words) == 0 {
			return nil, fmt.Errorf("anti-spam group %q has no terms", group.Label)
		}
	}
	if len(groups) == 0 {
		return nil, errors.New("anti-spam lexicon is empty")
	}
	return groups, nil
}

// parseGroupHeader splits "[博彩 40]" into label and points.
func parseGroupHeader(line string) (Group, error) {
	if !strings.HasSuffix(line, "]") {
		return Group{}, fmt.Errorf("%q is not a [label points] header", line)
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
	cut := strings.LastIndexAny(inner, " \t")
	if cut <= 0 {
		return Group{}, fmt.Errorf("%q needs a label and a score", line)
	}
	points, err := strconv.Atoi(strings.TrimSpace(inner[cut+1:]))
	if err != nil || points <= 0 || points > 1000 {
		return Group{}, fmt.Errorf("%q has no usable score", line)
	}
	label := strings.TrimSpace(inner[:cut])
	if label == "" {
		return Group{}, fmt.Errorf("%q has no label", line)
	}
	return Group{Label: label, Points: points}, nil
}

// LoadLexiconFile reads and parses an external word list.
func LoadLexiconFile(path string) ([]Group, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("spam: read lexicon %s: %w", path, err)
	}
	return ParseLexicon(string(text))
}

func mustParseLexicon(text string) []Group {
	groups, err := ParseLexicon(text)
	if err != nil {
		panic("spam: embedded lexicon: " + err.Error())
	}
	return groups
}

// contactPatterns are the ways spam asks the reader to continue elsewhere.
// Each is a shape, not a word, so it survives a change of slogan.
var contactPatterns = []struct {
	label  string
	points int
	re     *regexp.Regexp
}{
	// The rules that name a way to make contact are decisive on their own: no
	// ordinary comment on a technical blog contains one by accident.
	{"微信号", 60, regexp.MustCompile(`(?i)(微信|wechat|weixin|\bvx\b|\bwx\b|v信|威信|加我|加v[^a-z]|加我v)`)},
	{"Telegram", 60, regexp.MustCompile(`(?i)(t\.me/|telegram|电报|纸飞机)`)},
	{"手机号", 60, regexp.MustCompile(`(?:\+?86[- ]?)?1[3-9]\d{9}`)},
	{"QQ 号", 45, regexp.MustCompile(`(?i)(qq|扣扣)\s*[:：]?\s*\d{5,12}`)},
}

// botAgents are User-Agent fragments that announce a script. "bot" and "spider"
// catch the honest ones; the rest are HTTP libraries people post with.
var botAgents = []string{
	"curl", "wget", "python-requests", "python-urllib", "go-http-client",
	"okhttp", "java/", "libwww", "httpclient", "scrapy", "headlesschrome",
	"phantomjs", "zgrab", "masscan", "bot", "spider", "crawler", "slurp",
}

// shorteners hide the destination from the reader, which is the whole point of
// using one in a comment.
var shorteners = []string{
	"bit.ly", "t.co", "t.cn", "goo.gl", "tinyurl.com", "ow.ly", "is.gd",
	"buff.ly", "rb.gy", "cutt.ly", "shorturl.at", "dwz.cn", "url.cn",
	"suo.im", "s.id", "v.gd", "u.to", "clck.ru", "rebrand.ly",
}

// suspiciousTLDs are cheap to register in bulk, which is why spam farm
// addresses cluster on them. They are a signal, never a verdict.
var suspiciousTLDs = []string{
	".top", ".xyz", ".icu", ".cyou", ".buzz", ".click", ".work", ".loan",
	".win", ".bid", ".review", ".stream", ".gq", ".tk", ".ml", ".ga", ".cf",
	".rest", ".sbs", ".monster", ".quest",
}

// disposableDomains hand out an address that lives for minutes.
var disposableDomains = []string{
	"mailinator.com", "guerrillamail.com", "10minutemail.com", "tempmail.com",
	"trashmail.com", "yopmail.com", "sharklasers.com", "getnada.com",
	"dispostable.com", "maildrop.cc", "throwawaymail.com", "mailnesia.com",
	"spam4.me", "grr.la", "temp-mail.org", "fakeinbox.com", "mintemail.com",
}

// isShortener reports whether host is a link shortener or a subdomain of one.
func isShortener(host string) bool {
	return containsDomain(shorteners, host)
}

// hasSuspiciousTLD reports whether host ends in one of the bulk-registered TLDs.
func hasSuspiciousTLD(host string) bool {
	for _, tld := range suspiciousTLDs {
		if strings.HasSuffix(host, tld) {
			return true
		}
	}
	return false
}

// isDisposable reports whether the email domain is a throwaway service.
func isDisposable(domain string) bool {
	return containsDomain(disposableDomains, domain)
}

// containsDomain matches a host or any subdomain of a listed domain, so
// "mail.mailinator.com" is caught by "mailinator.com".
func containsDomain(list []string, host string) bool {
	host = strings.ToLower(strings.TrimPrefix(host, "."))
	for _, domain := range list {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
