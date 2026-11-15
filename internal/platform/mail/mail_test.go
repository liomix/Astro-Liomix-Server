package mail

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRenderBuildsMultipartMessage(t *testing.T) {
	t.Parallel()

	cfg := SMTPConfig{From: "blog@example.com", FromName: "博客"}
	msg := Message{
		To:      "reader@example.com",
		Subject: "有人回复了你的评论",
		Text:    "第一行\n第二行",
		HTML:    "<p>第一行</p>",
	}

	body, err := render(msg, cfg)
	if err != nil {
		t.Fatalf("render() error = %v", err)
	}
	raw := string(body)

	for _, want := range []string{
		"From: =?UTF-8?",
		"To: reader@example.com",
		"Subject: =?UTF-8?",
		"MIME-Version: 1.0",
		"multipart/alternative",
		"text/plain; charset=UTF-8",
		"text/html; charset=UTF-8",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("message does not contain %q:\n%s", want, raw)
		}
	}

	// Every line must end CRLF: a bare LF is how a message body becomes a
	// header on a permissive server.
	if strings.Contains(strings.ReplaceAll(raw, "\r\n", ""), "\n") {
		t.Error("message contains a bare LF")
	}
}

func TestRenderRejectsHeaderInjection(t *testing.T) {
	t.Parallel()

	_, err := render(Message{
		To:      "reader@example.com",
		Subject: "hello\r\nBcc: victim@example.com",
		Text:    "body",
	}, SMTPConfig{From: "blog@example.com"})
	if err == nil {
		t.Fatal("render() accepted a subject containing a newline")
	}
}

func TestQueueDrainsOnClose(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{}
	queue := NewQueue(sender, 8, nil)

	for i := 0; i < 5; i++ {
		if !queue.Enqueue(Message{To: "reader@example.com", Text: "hi"}) {
			t.Fatalf("Enqueue() = false at message %d", i)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := sender.count(); got != 5 {
		t.Errorf("sent %d messages, want 5", got)
	}
	if queue.Enqueue(Message{To: "reader@example.com"}) {
		t.Error("Enqueue() accepted a message after Close")
	}
}

func TestQueueDropsWhenFull(t *testing.T) {
	t.Parallel()

	blocked := make(chan struct{})
	sender := &blockingSender{release: blocked}
	queue := NewQueue(sender, 1, nil)

	// The worker takes the first message and blocks in Send, so the buffer
	// holds one more and everything after that is dropped rather than queued
	// without bound.
	accepted := 0
	for i := 0; i < 5; i++ {
		if queue.Enqueue(Message{To: "reader@example.com"}) {
			accepted++
		}
	}
	if accepted > 2 {
		t.Errorf("accepted %d messages into a queue of 1, want at most 2", accepted)
	}

	close(blocked)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

type recordingSender struct {
	mu   sync.Mutex
	sent []Message
}

func (s *recordingSender) Send(_ context.Context, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	return nil
}

func (s *recordingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

type blockingSender struct {
	release chan struct{}
}

func (s *blockingSender) Send(context.Context, Message) error {
	<-s.release
	return nil
}

func TestRenderEncodesBodiesAsBase64(t *testing.T) {
	t.Parallel()

	body, err := render(Message{
		To:      "reader@example.com",
		Subject: "hi",
		Text:    "第一行\n第二行",
		HTML:    "<p>第一行</p>",
	}, SMTPConfig{From: "blog@example.com"})
	if err != nil {
		t.Fatalf("render() error = %v", err)
	}
	raw := string(body)

	if strings.Contains(raw, "8bit") {
		t.Error("the message still declares 8bit; it must stay 7-bit clean")
	}
	const header = "Content-Transfer-Encoding: base64\r\n\r\n"
	start := strings.Index(raw, header)
	if start < 0 {
		t.Fatalf("no base64 part in:/n%s", raw)
	}
	payload := raw[start+len(header):]
	if end := strings.Index(payload, "\r\n--"); end >= 0 {
		payload = payload[:end]
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload, "\r\n", ""))
	if err != nil {
		t.Fatalf("decode part: %v", err)
	}
	if !strings.Contains(string(decoded), "第一行") {
		t.Errorf("decoded text part = %q, want the body back", decoded)
	}

	// Every line of the encoded payload must respect the 76-column convention
	// and the CRLF rule, which is what makes long HTML safe.
	for _, line := range strings.Split(strings.TrimSuffix(payload, "\r\n"), "\r\n") {
		if strings.ContainsAny(line, "\r\n") || len(line) > 76 {
			t.Errorf("base64 line is %d characters, want at most 76", len(line))
		}
	}
}

func TestRenderTemplate(t *testing.T) {
	t.Parallel()

	notice := CommentNotice{
		SiteName:     "博客",
		SiteURL:      "https://example.com",
		PostSlug:     "hello",
		AuthorName:   "Ada",
		AuthorEmail:  "ada@example.com",
		Body:         "第一行\n<b>不是标签</b>",
		ParentAuthor: "Grace",
		CreatedAt:    time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC),
	}
	body := `<p>{{siteName}}|{{postSlug}}|{{postURL}}|{{authorName}}|{{authorEmail}}|` +
		`{{parentAuthor}}|{{body}}|{{time}}|{{nope}}</p>`

	got := RenderTemplate(body, notice)

	for _, want := range []string{
		"博客",
		"hello",
		"https://example.com/posts/hello",
		"Ada",
		"ada@example.com",
		"Grace",
		// The comment body is operator-visible text, not markup: a reader's
		// literal "<b>" must arrive as characters, not as a tag.
		"&lt;b&gt;不是标签&lt;/b&gt;",
		notice.CreatedAt.Local().Format("2006-01-02 15:04"),
		// An unknown token stays as written so a typo is visible.
		"{{nope}}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered body does not contain %q:/n%s", want, got)
		}
	}
}

func TestWithCustomBodyFallsBackToBuiltIn(t *testing.T) {
	t.Parallel()

	notice := CommentNotice{SiteName: "博客", PostSlug: "hello", AuthorName: "Ada", Body: "hi"}
	builtin := NewReply(notice)

	// An empty setting keeps the compiled template.
	if got := WithCustomBody(builtin, "  \n ", notice); got.HTML != builtin.HTML {
		t.Error("an empty body replaced the built-in HTML")
	}

	custom := WithCustomBody(builtin, "<p>只有这一段 {{authorName}}</p>", notice)
	if !strings.Contains(custom.HTML, "只有这一段 Ada") {
		t.Errorf("custom HTML = %q, want the operator's document", custom.HTML)
	}
	// The plain-text part survives: it is the fallback for a client that shows
	// no HTML at all.
	if custom.Text == "" {
		t.Error("the plain-text part was dropped")
	}
	if custom.Subject != builtin.Subject {
		t.Errorf("subject = %q, want the built-in one", custom.Subject)
	}
}
