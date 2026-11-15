package spam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseVerdict(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		answer string
		spam   bool
		ok     bool
	}{
		{"bare json", `{"spam":true,"reason":"广告"}`, true, true},
		{"fenced json", "```json\n{\"spam\":false,\"reason\":\"正常讨论\"}\n```", false, true},
		{"prose around it", `判断如下：{"spam":true,"reason":"外链推广"} 以上。`, true, true},
		{"missing field", `{"reason":"不知道"}`, false, false},
		{"not json", "无法判断", false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verdict, err := parseVerdict(tc.answer)
			if tc.ok != (err == nil) {
				t.Fatalf("parseVerdict() error = %v, want ok=%v", err, tc.ok)
			}
			if tc.ok && verdict.Spam != tc.spam {
				t.Errorf("Spam = %v, want %v", verdict.Spam, tc.spam)
			}
		})
	}
}

func TestLLMCheck(t *testing.T) {
	t.Parallel()

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var req llmRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "test-model" {
			t.Errorf("model = %q, want test-model", req.Model)
		}
		if len(req.Messages) != 2 {
			t.Errorf("messages = %d, want a system and a user message", len(req.Messages))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": "```json\n{\"spam\":true,\"reason\":\"纯广告\"}\n```"},
			}},
		})
	}))
	defer server.Close()

	checker, err := NewLLM(server.URL, "secret", "test-model", 2*time.Second)
	if err != nil {
		t.Fatalf("NewLLM() error = %v", err)
	}
	verdict, err := checker.Check(context.Background(), Content{
		Author: "张三", Email: "a@example.com", Body: "买我的课程", PostSlug: "hello",
	})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !verdict.Spam {
		t.Error("Check() = ham for an obvious advert")
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
}

func TestLLMCheckReportsUnavailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer server.Close()

	checker, err := NewLLM(server.URL, "", "test-model", 2*time.Second)
	if err != nil {
		t.Fatalf("NewLLM() error = %v", err)
	}
	if _, err := checker.Check(context.Background(), Content{Body: "hi"}); err == nil {
		t.Fatal("Check() error = nil for a 429 answer")
	}
}

func TestNewAkismetRequiresKeyAndSite(t *testing.T) {
	t.Parallel()

	if _, err := NewAkismet("", "https://example.com", time.Second); err == nil {
		t.Error("NewAkismet() accepted an empty key")
	}
	if _, err := NewAkismet("key", "", time.Second); err == nil {
		t.Error("NewAkismet() accepted an empty site URL")
	}
}

func TestAkismetNeedsClientIP(t *testing.T) {
	t.Parallel()

	checker, err := NewAkismet("key", "https://example.com", time.Second)
	if err != nil {
		t.Fatalf("NewAkismet() error = %v", err)
	}
	// Without an IP the API refuses the request, so the answer must be an
	// error the caller can recognise — not a silent "ham".
	if _, err := checker.Check(context.Background(), Content{Body: "hi"}); err == nil {
		t.Fatal("Check() error = nil without a client IP")
	}
}
