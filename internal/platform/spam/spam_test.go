package spam_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/spam"
)

// browser is a User-Agent a real visitor has. Almost every rule that looks at
// the client depends on it being present, so tests that are not about the
// client rule use it.
const browser = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/131.0 Safari/537.36"

// fixtureLexicon is what the behaviour tests match against.
//
// They must not depend on the shipped word list: editing anti-spam-1.txt would
// otherwise break unrelated tests, and the list is meant to be edited freely.
// One test below checks the shipped file itself, nothing else uses it.
func fixtureLexicon() []spam.Group {
	return []spam.Group{
		{Label: "博彩", Points: 40, Words: []string{"赌场", "casino"}},
		{Label: "推广", Points: 30, Words: []string{"优惠券"}},
	}
}

// newFilter is the engine under test, wired to the fixture list.
func newFilter(opts ...spam.Option) *spam.Filter {
	return spam.New(append([]spam.Option{spam.WithLexicon(fixtureLexicon())}, opts...)...)
}

func clean() spam.Content {
	return spam.Content{
		Author:    "小明",
		Email:     "xiaoming@example.com",
		Body:      "这篇讲得很清楚，第 3 节的例子我照着跑通了，谢谢。",
		PostSlug:  "hello-world",
		IP:        "203.0.113.7",
		UserAgent: browser,
		Referer:   "https://example.com/posts/hello-world",
	}
}

func TestFilterVerdicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*spam.Content)
		want   bool
	}{
		{
			name:   "ordinary comment",
			mutate: func(*spam.Content) {},
			want:   false,
		},
		{
			name: "one link to the author's own blog",
			mutate: func(c *spam.Content) {
				c.Body = "补充一个细节：https://myblog.example.com/notes 里也提到了"
			},
			want: false,
		},
		{
			name: "two links is still plausible",
			mutate: func(c *spam.Content) {
				c.Body = "参考 https://example.com/a 和 https://example.com/b 两篇"
			},
			want: false,
		},
		{
			name: "wechat advert",
			mutate: func(c *spam.Content) {
				c.Body = "加微信 abc12345，带你上车"
			},
			want: true,
		},
		{
			name: "gambling advert with links",
			mutate: func(c *spam.Content) {
				c.Body = "真人荷官在线发牌 澳门赌场 https://spam.top/a https://spam.top/b"
			},
			want: true,
		},
		{
			name: "disposable email plus promo",
			mutate: func(c *spam.Content) {
				c.Email = "throwaway@mailinator.com"
				c.Body = "优惠券链接 https://example.com/coupon"
			},
			want: true,
		},
		{
			name: "script bot without user agent",
			mutate: func(c *spam.Content) {
				c.UserAgent = ""
				c.Body = "hello"
			},
			want: false, // 30 points: a signal, not a verdict
		},
		{
			name: "script bot pasting links",
			mutate: func(c *spam.Content) {
				c.UserAgent = "curl/8.4.0"
				c.Body = "see https://a.example.com https://b.example.com https://c.example.com https://d.example.com"
			},
			want: true,
		},
		{
			name: "script tag in the body",
			mutate: func(c *spam.Content) {
				c.Body = `<script>fetch("//evil")</script>`
			},
			want: true,
		},
		{
			name: "name field is an advert",
			mutate: func(c *spam.Content) {
				c.Author = "VX:abc12345 加我"
			},
			want: true,
		},
		{
			name: "phone number in the body",
			mutate: func(c *spam.Content) {
				c.Body = "需要合作打电话 13800138000 详谈"
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			content := clean()
			tt.mutate(&content)

			verdict, err := newFilter().Check(t.Context(), content)
			if err != nil {
				t.Fatalf("Check() error = %v", err)
			}
			if verdict.Spam != tt.want {
				t.Fatalf("Spam = %v (score %d, reasons %v), want %v",
					verdict.Spam, verdict.Score, verdict.Reasons, tt.want)
			}
			if verdict.Spam != (verdict.Score >= spam.DefaultConfig().Threshold) {
				t.Errorf("verdict and score disagree: score %d, spam %v", verdict.Score, verdict.Spam)
			}
		})
	}
}

func TestFilterReportsWhyItFired(t *testing.T) {
	t.Parallel()

	content := clean()
	content.Body = "加微信 abc12345，澳门赌场 https://spam.top/"
	verdict, err := newFilter().Check(t.Context(), content)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if len(verdict.Reasons) < 2 {
		t.Errorf("Reasons = %v, want one entry per rule that fired", verdict.Reasons)
	}
	joined := strings.Join(verdict.Reasons, " / ")
	for _, want := range []string{"微信", "博彩"} {
		if !strings.Contains(joined, want) {
			t.Errorf("reasons %q do not mention %q", joined, want)
		}
	}
}

func TestFilterDuplicateWithinWindow(t *testing.T) {
	t.Parallel()

	clock := &stepClock{at: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	filter := spam.New(spam.WithClock(clock.now))
	content := clean()
	content.UserAgent = browser

	first, err := filter.Check(t.Context(), content)
	if err != nil {
		t.Fatalf("first Check() error = %v", err)
	}
	if first.Spam {
		t.Fatalf("the first copy was already spam: score %d, %v", first.Score, first.Reasons)
	}

	clock.advance(time.Minute)
	second, err := filter.Check(t.Context(), content)
	if err != nil {
		t.Fatalf("second Check() error = %v", err)
	}
	if !second.Spam {
		t.Fatalf("the same body twice was not spam: score %d, %v", second.Score, second.Reasons)
	}

	// Past the window the history is gone, so the same text is new again.
	clock.advance(20 * time.Minute)
	third, err := filter.Check(t.Context(), content)
	if err != nil {
		t.Fatalf("third Check() error = %v", err)
	}
	if third.Spam {
		t.Errorf("the duplicate rule outlived its window: score %d, %v", third.Score, third.Reasons)
	}
}

func TestFilterFloodPerAddress(t *testing.T) {
	t.Parallel()

	clock := &stepClock{at: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	tuning := spam.DefaultConfig()
	tuning.MaxPerIP = 2
	filter := spam.New(spam.WithConfig(tuning), spam.WithClock(clock.now))

	for i := 1; i <= 2; i++ {
		content := clean()
		// Distinct bodies, so the duplicate rule stays out of the way and only
		// volume is being measured.
		content.Body = strings.Repeat("这是第几条评论 ", i+1)
		if verdict, err := filter.Check(t.Context(), content); err != nil {
			t.Fatalf("Check() #%d error = %v", i, err)
		} else if verdict.Spam {
			t.Fatalf("comment #%d under the limit was flagged: %d %v", i, verdict.Score, verdict.Reasons)
		}
		clock.advance(10 * time.Second)
	}

	content := clean()
	content.Body = "第 3 条，还是同一个地址"
	verdict, err := filter.Check(t.Context(), content)
	if err != nil {
		t.Fatalf("Check() over the limit error = %v", err)
	}
	if !verdict.Spam {
		t.Fatalf("the 3rd comment from one address was not flagged: %d %v", verdict.Score, verdict.Reasons)
	}
}

func TestFilterRespectsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := newFilter().Check(ctx, clean()); err == nil {
		t.Fatal("Check() on a cancelled context returned a verdict")
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	valid := spam.DefaultConfig()
	if err := valid.Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}

	cases := map[string]func(*spam.Config){
		"zero threshold":   func(c *spam.Config) { c.Threshold = 0 },
		"zero window":      func(c *spam.Config) { c.Window = 0 },
		"zero per ip":      func(c *spam.Config) { c.MaxPerIP = 0 },
		"zero per email":   func(c *spam.Config) { c.MaxPerEmail = 0 },
		"zero max tracked": func(c *spam.Config) { c.MaxTracked = 0 },
	}
	for name, mutate := range cases {
		cfg := valid
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", name)
		}
	}
}

func TestInvalidConfigFallsBackToDefaults(t *testing.T) {
	t.Parallel()

	// A zero threshold would mark everything as spam, so an invalid config must
	// not be applied at all.
	verdict, err := spam.New(spam.WithConfig(spam.Config{})).Check(t.Context(), clean())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if verdict.Spam {
		t.Errorf("an invalid config was applied: score %d, %v", verdict.Score, verdict.Reasons)
	}
}

// stepClock is a manually advanced clock, so window behaviour is tested without
// sleeping.
type stepClock struct {
	at time.Time
}

func (c *stepClock) now() time.Time { return c.at }

func (c *stepClock) advance(d time.Duration) { c.at = c.at.Add(d) }

func TestParseLexicon(t *testing.T) {
	t.Parallel()

	groups, err := spam.ParseLexicon(`
# 注释行会被跳过
[博彩/色情/药品 40]
赌场
Casino

[推广 30]
代发
`)
	if err != nil {
		t.Fatalf("ParseLexicon() error = %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if groups[0].Label != "博彩/色情/药品" || groups[0].Points != 40 {
		t.Errorf("first group = %+v, want the label with a slash and 40", groups[0])
	}
	// Terms are lower-cased on the way in, so the matcher never has to care.
	if len(groups[0].Words) != 2 || groups[0].Words[1] != "casino" {
		t.Errorf("words = %v, want them lower-cased", groups[0].Words)
	}
	if groups[1].Label != "推广" || groups[1].Points != 30 {
		t.Errorf("second group = %+v", groups[1])
	}
}

func TestParseLexiconRejectsBrokenFiles(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":               "",
		"term before header":  "赌场\n[博彩 40]\n",
		"header no score":     "[博彩]\n赌场\n",
		"score not a number":  "[博彩 高]\n赌场\n",
		"score out of range":  "[博彩 0]\n赌场\n",
		"group without terms": "[博彩 40]\n\n[推广 30]\n代发\n",
	}
	for name, text := range cases {
		if _, err := spam.ParseLexicon(text); err == nil {
			t.Errorf("%s: ParseLexicon() = nil, want an error", name)
		}
	}
}

func TestDefaultLexiconIsUsable(t *testing.T) {
	t.Parallel()

	groups := spam.DefaultLexicon()
	if len(groups) == 0 {
		t.Fatal("the embedded word list is empty")
	}
	terms := 0
	for _, group := range groups {
		terms += len(group.Words)
	}
	if terms < 40 {
		t.Errorf("embedded lexicon has %d terms, want a usable list", terms)
	}
}

func TestFilterUsesSuppliedLexicon(t *testing.T) {
	t.Parallel()

	custom := []spam.Group{{Label: "测试", Points: 60, Words: []string{"特定词"}}}
	filter := spam.New(spam.WithLexicon(custom))

	content := clean()
	content.Body = "这里提到特定词"
	verdict, err := filter.Check(t.Context(), content)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if verdict.Score < 60 {
		t.Errorf("a supplied term did not fire: %d %v", verdict.Score, verdict.Reasons)
	}

	// The supplied list replaces the default one rather than adding to it.
	other := clean()
	other.Body = "澳门赌场"
	verdict, err = filter.Check(t.Context(), other)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if verdict.Score != 0 {
		t.Errorf("the default list is still active: %d %v", verdict.Score, verdict.Reasons)
	}
}

func TestSoftEvidenceNeedsTwoKinds(t *testing.T) {
	t.Parallel()

	// One word worth the whole threshold: heavy, but it is one kind of
	// evidence, so it cannot decide on its own.
	heavy := []spam.Group{{Label: "重词", Points: 60, Words: []string{"特定词"}}}
	filter := spam.New(spam.WithLexicon(heavy))

	content := clean()
	content.Body = "这里提到特定词"
	verdict, err := filter.Check(t.Context(), content)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if verdict.Spam {
		t.Fatalf("a single soft category decided the verdict: %d %v", verdict.Score, verdict.Reasons)
	}
	if verdict.Score < spam.DefaultConfig().Threshold {
		t.Fatalf("score = %d, want the heavy word to have reached the threshold", verdict.Score)
	}

	// A second, different kind of evidence tips it.
	content.Email = "throwaway@mailinator.com"
	verdict, err = filter.Check(t.Context(), content)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !verdict.Spam {
		t.Errorf("two kinds of soft evidence did not decide: %d %v", verdict.Score, verdict.Reasons)
	}
}

func TestFilterSeesThroughObfuscation(t *testing.T) {
	t.Parallel()

	content := clean()
	// A zero-width space inside the term, and a full-width spelling of the other
	// one: both are invisible or identical to a reader.
	content.Body = "ｃａｓｉｎｏ 很好玩，赌\u200b场也行"

	verdict, err := newFilter().Check(t.Context(), content)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if verdict.Score == 0 {
		t.Fatalf("the obfuscated terms were not matched at all: %v", verdict.Reasons)
	}
	if !strings.Contains(strings.Join(verdict.Reasons, " "), "词") {
		t.Errorf("reasons = %v, want a lexicon hit", verdict.Reasons)
	}
}
