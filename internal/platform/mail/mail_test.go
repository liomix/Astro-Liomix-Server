package mail

import (
	"context"
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
