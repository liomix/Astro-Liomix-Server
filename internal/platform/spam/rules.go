package spam

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// kind separates the two ways a rule can matter.
//
// A hard rule is evidence that stands on its own: a comment carrying a WeChat id
// is an advert whatever else it says. A soft rule is a signal that is plausible
// by itself — a link, a long body, a nickname made of digits — and only means
// something together with another one. Keeping the two apart is what stops two
// weak signals from adding up into a false positive.
type kind int

const (
	soft kind = iota
	hard
)

// finding is one rule's contribution.
type finding struct {
	kind kind
	// points is what the hit adds to the score. It is reported even for a hard
	// hit, where the score is not what decided.
	points int
	// category groups hits that are the same kind of evidence. The soft gate
	// counts distinct categories, not hits: two keyword groups firing are one
	// argument, not two.
	category string
	reason   string
}

// ruleFunc inspects one comment. A quiet rule returns nothing.
//
// The lexicon arrives as an argument rather than from a package-level global so
// that a test — and a deployment with its own word list — can hand in a
// different one without touching the engine.
type ruleFunc func(c Content, s stats, cfg Config, lex []Group) []finding

// rules is the pipeline. The order only decides the order of the reasons in the
// log; every rule sees the same comment.
var rules = []ruleFunc{
	linkRule,
	suspiciousLinkRule,
	keywordRule,
	contactRule,
	markupRule,
	repetitionRule,
	mixedScriptRule,
	capsRule,
	emailRule,
	websiteRule,
	authorRule,
	clientRule,
	lengthRule,
	duplicateRule,
	floodRule,
}

// linkRule scores addresses in the body. A comment with a link to its author's
// own blog is ordinary; four of them is a signature.
func linkRule(c Content, _ stats, _ Config, _ []Group) []finding {
	n := countLinks(c.Body)
	if n == 0 {
		return nil
	}
	points := n * 10
	if points > 40 {
		points = 40
	}
	return []finding{{soft, points, "links", fmt.Sprintf("正文含 %d 个链接", n)}}
}

// suspiciousLinkRule looks at where the links point. A comment can carry one
// address and still be an advert if that address is a shortener or a throwaway
// domain: the shortening is what hides the destination from a reader.
func suspiciousLinkRule(c Content, _ stats, _ Config, _ []Group) []finding {
	for _, host := range linkHosts(c.Body + " " + c.Website) {
		switch {
		case isShortener(host):
			return []finding{{soft, 35, "domain", "链接指向短链服务 " + host}}
		case hasSuspiciousTLD(host):
			return []finding{{soft, 30, "domain", "链接域名可疑 " + host}}
		}
	}
	return nil
}

// keywordRule is the word list. It fires once per category, so a wall of the
// same word does not multiply the score.
//
// Every hit is soft, and that is the whole point of keeping a word list at all:
// words say what a comment talks about, and an honest comment may talk about
// anything. A term can raise a score; it can never decide one.
func keywordRule(c Content, _ stats, _ Config, lex []Group) []finding {
	body := strings.ToLower(c.Body)
	var out []finding
	for _, group := range lex {
		for _, word := range group.Words {
			if strings.Contains(body, word) {
				out = append(out, finding{soft, group.Points, "words", "命中" + group.Label + "词：" + word})
				break
			}
		}
	}
	return out
}

// contactRule looks for the shapes spam uses to move a conversation off site:
// a WeChat id, a Telegram link, a phone number, a QQ number.
//
// These are hard: none of them appears in an ordinary comment by accident, and
// being wrong costs one click in the queue rather than a lost comment.
func contactRule(c Content, _ stats, _ Config, _ []Group) []finding {
	text := c.Author + "\n" + c.Body
	var out []finding
	for _, pattern := range contactPatterns {
		if pattern.re.MatchString(text) {
			out = append(out, finding{hard, pattern.points, "contact", "正文含" + pattern.label})
		}
	}
	return out
}

// markupRule catches an attempt to inject something a comment field should
// never contain. It is not a security control — the renderer escapes — but it
// is a strong signal about intent.
func markupRule(c Content, _ stats, _ Config, _ []Group) []finding {
	lowered := strings.ToLower(c.Body)
	for _, marker := range executableMarkers {
		if strings.Contains(lowered, marker) {
			return []finding{{hard, 60, "markup", "正文含可执行标记 " + marker}}
		}
	}
	return nil
}

// repetitionRule catches the "buy buy buy buy" shape: long text with almost no
// distinct characters.
func repetitionRule(c Content, _ stats, _ Config, _ []Group) []finding {
	runes := []rune(strings.ReplaceAll(c.Body, " ", ""))
	if len(runes) < 30 {
		return nil
	}
	distinct := make(map[rune]struct{}, len(runes))
	for _, r := range runes {
		distinct[r] = struct{}{}
	}
	if float64(len(distinct))/float64(len(runes)) < 0.35 {
		return []finding{{soft, 25, "repetition", "字符重复度过高"}}
	}
	return nil
}

// mixedScriptRule catches Latin words spelled with Cyrillic look-alikes, which
// is how a filter trained on ASCII words gets walked past.
func mixedScriptRule(c Content, _ stats, _ Config, _ []Group) []finding {
	words := strings.FieldsFunc(c.Body, func(r rune) bool {
		return !unicode.IsLetter(r)
	})
	for _, word := range words {
		var latin, cyrillic int
		for _, r := range word {
			switch {
			case unicode.Is(unicode.Cyrillic, r):
				cyrillic++
			case r < 0x250 && unicode.IsLetter(r):
				latin++
			}
		}
		if latin >= 2 && cyrillic >= 2 {
			return []finding{{soft, 25, "script", "单词混用西里尔字符 " + word}}
		}
	}
	return nil
}

// capsRule is the shouting rule. Chinese has no case, so it only ever fires on
// Latin text, which is where it is meaningful.
func capsRule(c Content, _ stats, _ Config, _ []Group) []finding {
	var letters, upper int
	for _, r := range c.Body {
		if !unicode.IsLetter(r) || r > unicode.MaxASCII {
			continue
		}
		letters++
		if unicode.IsUpper(r) {
			upper++
		}
	}
	if letters < 20 {
		return nil
	}
	if float64(upper)/float64(letters) > 0.7 {
		return []finding{{soft, 15, "caps", "大写字母比例过高"}}
	}
	return nil
}

// emailRule rejects the addresses people use for a single confirmation.
func emailRule(c Content, _ stats, _ Config, _ []Group) []finding {
	at := strings.LastIndex(c.Email, "@")
	if at < 0 {
		return nil
	}
	domain := strings.ToLower(c.Email[at+1:])
	if isDisposable(domain) {
		return []finding{{soft, 40, "email", "使用一次性邮箱 " + domain}}
	}
	return nil
}

func websiteRule(c Content, _ stats, _ Config, _ []Group) []finding {
	host := hostOf(strings.TrimSpace(c.Website))
	switch {
	case host == "":
		return nil
	case isShortener(host):
		return []finding{{soft, 30, "website", "网站字段是短链 " + host}}
	case hasSuspiciousTLD(host):
		return []finding{{soft, 25, "website", "网站字段域名可疑 " + host}}
	}
	return nil
}

// authorRule looks at the name field, which a bot fills with an advert because
// it renders above the comment.
func authorRule(c Content, _ stats, _ Config, _ []Group) []finding {
	var out []finding
	if countLinks(c.Author) > 0 {
		// A nickname carrying a URL is not a name, and it is shown to every
		// reader: hard, not a hint.
		out = append(out, finding{hard, 60, "author", "昵称里带链接"})
	}
	if countDigits(c.Author) >= 8 {
		out = append(out, finding{soft, 20, "author", "昵称里是一串数字"})
	}
	if len([]rune(c.Author)) > 40 {
		out = append(out, finding{soft, 10, "author", "昵称过长"})
	}
	return out
}

// clientRule reads the User-Agent. A browser always sends one; a script that
// posts comments usually does not bother, or announces itself.
func clientRule(c Content, _ stats, _ Config, _ []Group) []finding {
	agent := strings.ToLower(c.UserAgent)
	if strings.TrimSpace(agent) == "" {
		return []finding{{soft, 30, "client", "请求没有 User-Agent"}}
	}
	for _, marker := range botAgents {
		if strings.Contains(agent, marker) {
			return []finding{{soft, 30, "client", "User-Agent 像脚本：" + marker}}
		}
	}
	return nil
}

func lengthRule(c Content, _ stats, _ Config, _ []Group) []finding {
	if n := len([]rune(c.Body)); n > maxBodyRunes {
		return []finding{{soft, 15, "length", fmt.Sprintf("正文 %d 字，超过常规长度", n)}}
	}
	return nil
}

// duplicateRule is the strongest single signal: the same text submitted twice
// inside the window is a script retrying, whatever it says.
func duplicateRule(_ Content, s stats, _ Config, _ []Group) []finding {
	if s.duplicates == 0 {
		return nil
	}
	return []finding{{hard, 60, "duplicate", fmt.Sprintf("同样的正文在窗口内已出现 %d 次", s.duplicates)}}
}

// floodRule is the volume rule: one address or one email posting faster than a
// person reads.
//
// The address rule is hard — a dozen submissions from one client in ten minutes
// is not reading. The email rule is soft, because one address shared by a
// household can produce a burst of legitimate replies.
func floodRule(_ Content, s stats, cfg Config, _ []Group) []finding {
	window := cfg.Window.Round(time.Minute)
	var out []finding
	if s.ipCount > cfg.MaxPerIP {
		out = append(out, finding{hard, 60, "flood", fmt.Sprintf("同一 IP 在 %s 内第 %d 条", window, s.ipCount)})
	}
	if s.emailCount > cfg.MaxPerEmail {
		out = append(out, finding{soft, 45, "flood", fmt.Sprintf("同一邮箱在 %s 内第 %d 条", window, s.emailCount)})
	}
	return out
}

// countDigits counts ASCII digits, which is all "this field is a phone number"
// needs.
func countDigits(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

// executableMarkers are lowercase fragments no honest comment carries.
var executableMarkers = []string{"<script", "<iframe", "javascript:", " onerror=", " onload="}
