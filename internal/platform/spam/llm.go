package spam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLM classifies comments with an OpenAI-compatible chat completions endpoint.
//
// "OpenAI-compatible" is the useful lowest common denominator: it covers the
// hosted API, a local llama.cpp/ollama server, and most gateways. The prompt
// asks for a small JSON object, and the parser is tolerant about everything
// around it — models decorate answers with prose and code fences even when told
// not to.
type LLM struct {
	endpoint string
	key      string
	model    string
	client   *http.Client
}

// NewLLM returns a checker. An empty key is allowed: a local server often takes
// none, and the Authorization header is then omitted.
func NewLLM(endpoint, key, model string, timeout time.Duration) (*LLM, error) {
	endpoint = strings.TrimSpace(endpoint)
	model = strings.TrimSpace(model)
	if endpoint == "" {
		return nil, fmt.Errorf("llm: endpoint is required")
	}
	if model == "" {
		return nil, fmt.Errorf("llm: model is required")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &LLM{
		endpoint: endpoint,
		key:      strings.TrimSpace(key),
		model:    model,
		client:   &http.Client{Timeout: timeout},
	}, nil
}

// prompt is deliberately blunt. A classifier that is asked to "consider
// carefully" writes essays; this one is asked for one field and one sentence.
const systemPrompt = `你是博客评论的垃圾内容分类器。判断给定评论是否为垃圾/广告/诈骗/无意义灌水/纯链接推广。
只输出一个 JSON 对象，不要解释、不要代码块：{"spam": true 或 false, "reason": "不超过20字的理由"}
判断依据：真实讨论文章内容为正常；推销商品、拉人进群、SEO 外链、赌博色情、纯表情或乱码为垃圾。
无法判断时输出 {"spam": false, "reason": "无法判断"}。`

type llmRequest struct {
	Model       string       `json:"model"`
	Temperature float64      `json:"temperature"`
	Messages    []llmMessage `json:"messages"`
}

type llmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type llmResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Check asks the model whether the comment is spam.
func (l *LLM) Check(ctx context.Context, c Content) (Verdict, error) {
	payload := llmRequest{
		Model:       l.model,
		Temperature: 0,
		Messages: []llmMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: llmContent(c)},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Verdict{}, fmt.Errorf("llm: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint, bytes.NewReader(body))
	if err != nil {
		return Verdict{}, fmt.Errorf("llm: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if l.key != "" {
		req.Header.Set("Authorization", "Bearer "+l.key)
	}

	resp, err := l.client.Do(req)
	if err != nil {
		return Verdict{}, fmt.Errorf("%w: llm: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Verdict{}, fmt.Errorf("%w: llm: read response: %v", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return Verdict{}, fmt.Errorf("%w: llm %d: %s", ErrUnavailable, resp.StatusCode, truncateDescription(raw, 160))
	}

	var decoded llmResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Verdict{}, fmt.Errorf("%w: llm: decode response: %v", ErrUnavailable, err)
	}
	if decoded.Error != nil {
		return Verdict{}, fmt.Errorf("%w: llm: %s", ErrUnavailable, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return Verdict{}, fmt.Errorf("%w: llm: empty choices", ErrUnavailable)
	}

	verdict, err := parseVerdict(decoded.Choices[0].Message.Content)
	if err != nil {
		return Verdict{}, fmt.Errorf("%w: llm: %v", ErrUnavailable, err)
	}
	return verdict, nil
}

// llmContent lays the comment out as labelled lines. Labels matter: a model
// shown "作者：xxx\n正文：..." is far less likely to treat a name as content.
func llmContent(c Content) string {
	var b strings.Builder
	fmt.Fprintf(&b, "文章：%s\n", trim(c.PostSlug, 200))
	fmt.Fprintf(&b, "作者：%s\n", trim(c.Author, 100))
	if email := strings.TrimSpace(c.Email); email != "" {
		fmt.Fprintf(&b, "邮箱：%s\n", trim(email, 254))
	}
	if site := strings.TrimSpace(c.Website); site != "" {
		fmt.Fprintf(&b, "网站：%s\n", trim(site, 200))
	}
	fmt.Fprintf(&b, "正文：\n%s", trim(c.Body, 4000))
	return b.String()
}

// parseVerdict extracts the JSON object from a model answer that may contain
// prose, code fences, or both.
func parseVerdict(answer string) (Verdict, error) {
	start := strings.Index(answer, "{")
	end := strings.LastIndex(answer, "}")
	if start < 0 || end <= start {
		return Verdict{}, fmt.Errorf("no JSON object in answer %q", truncateDescription([]byte(answer), 120))
	}

	var decoded struct {
		Spam   *bool  `json:"spam"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(answer[start:end+1]), &decoded); err != nil {
		return Verdict{}, fmt.Errorf("decode answer: %v", err)
	}
	if decoded.Spam == nil {
		return Verdict{}, fmt.Errorf("answer has no spam field: %q", truncateDescription([]byte(answer), 120))
	}
	return Verdict{Spam: *decoded.Spam, Reason: truncateDescription([]byte(decoded.Reason), 80)}, nil
}
