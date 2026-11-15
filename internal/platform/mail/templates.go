package mail

import (
	"bytes"
	"html"
	htmltemplate "html/template"
	"strings"
	"time"
)

// CommentNotice is everything a notification needs, expressed without any
// reference to the comment module.
//
// The translation from a comment to this struct happens in cmd/server, which is
// the only package allowed to know about both. That keeps this package reusable
// and keeps the comment module free of email concerns.
type CommentNotice struct {
	SiteName string
	SiteURL  string
	// ConsoleURL is where an operator reviews the comment. Empty disables the
	// link, which is the right answer for a deployment without the console.
	ConsoleURL string
	PostSlug    string
	AuthorName  string
	AuthorEmail string
	Body        string
	CreatedAt   time.Time
	// ParentAuthor is the commenter being replied to. Empty for a top-level
	// comment, which is what makes the two messages different.
	ParentAuthor string
}

// PostURL is the public address of the post, or the site root when no post URL
// can be built. A link that 404s is worse than a link to the front page.
func (n CommentNotice) PostURL() string {
	if n.SiteURL == "" {
		return ""
	}
	if n.PostSlug == "" {
		return n.SiteURL + "/"
	}
	return n.SiteURL + "/posts/" + n.PostSlug
}

// NewCommentForAdmin builds the "a comment arrived" notice.
func NewCommentForAdmin(n CommentNotice) Message {
	var text bytes.Buffer
	name := fallback(n.SiteName, "站点")
	writeText(&text, "有新评论：")
	writeText(&text, n.AuthorName+" 在 "+n.PostSlug+" 留言：")
	writeText(&text, "")
	writeText(&text, n.Body)
	writeText(&text, "")
	if link := n.PostURL(); link != "" {
		writeText(&text, "文章："+link)
	}
	if n.ConsoleURL != "" {
		writeText(&text, "审核："+n.ConsoleURL)
	}

	return Message{
		Subject: "[" + name + "] 新评论：" + fallback(n.AuthorName, "访客") + " · " + n.PostSlug,
		Text:    text.String(),
		HTML:    renderHTML(adminTemplate, n),
	}
}

// NewReply builds the "someone replied to you" notice.
func NewReply(n CommentNotice) Message {
	var text bytes.Buffer
	name := fallback(n.SiteName, "站点")
	writeText(&text, fallback(n.ParentAuthor, "你")+"，"+n.AuthorName+" 回复了你在《"+n.PostSlug+"》下的评论：")
	writeText(&text, "")
	writeText(&text, n.Body)
	writeText(&text, "")
	if link := n.PostURL(); link != "" {
		writeText(&text, "查看并回复："+link)
	}
	writeText(&text, "")
	writeText(&text, "此邮件由 "+name+" 自动发送，请勿直接回复。")

	return Message{
		Subject: "[" + name + "] " + fallback(n.AuthorName, "有人") + " 回复了你的评论",
		Text:    text.String(),
		HTML:    renderHTML(replyTemplate, n),
	}
}

func writeText(buf *bytes.Buffer, line string) {
	buf.WriteString(line)
	buf.WriteString("\n")
}

func fallback(value, when string) string {
	if strings.TrimSpace(value) == "" {
		return when
	}
	return value
}

func renderHTML(tmpl *htmltemplate.Template, n CommentNotice) string {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, n); err != nil {
		// The templates are compiled into the binary and only read fields that
		// always exist, so this cannot happen outside a programming mistake.
		// Returning an empty body makes the mail sender refuse the message,
		// which is a logged drop rather than a malformed email.
		return ""
	}
	return buf.String()
}

// The HTML bodies use inline styles and no external resources: mail clients
// strip <style> blocks and block remote images, so a stylesheet would simply
// not arrive.
var adminTemplate = htmltemplate.Must(htmltemplate.New("admin").Parse(`<!DOCTYPE html>
<html lang="zh-CN"><body style="margin:0;padding:24px;background:#f6f7f9;font-family:-apple-system,'Segoe UI',Roboto,'Helvetica Neue',Arial,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2328;">
<div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e5e7eb;border-radius:12px;padding:24px;">
  <div style="font-size:13px;color:#6b7280;margin-bottom:8px;">{{.SiteName}}</div>
  <h1 style="margin:0 0 16px;font-size:18px;line-height:1.4;">新评论{{if .PostSlug}} · {{.PostSlug}}{{end}}</h1>
  <div style="font-size:13px;color:#6b7280;margin-bottom:12px;">{{.AuthorName}}</div>
  <blockquote style="margin:0 0 16px;padding:12px 16px;background:#f6f7f9;border-left:3px solid #2563eb;border-radius:4px;white-space:pre-wrap;font-size:14px;line-height:1.6;">{{.Body}}</blockquote>
  {{if .PostURL}}<p style="margin:0 0 8px;"><a href="{{.PostURL}}" style="color:#2563eb;text-decoration:none;">查看文章</a></p>{{end}}
  {{if .ConsoleURL}}<p style="margin:0;"><a href="{{.ConsoleURL}}" style="color:#2563eb;text-decoration:none;">去审核</a></p>{{end}}
</div>
</body></html>`))

var replyTemplate = htmltemplate.Must(htmltemplate.New("reply").Parse(`<!DOCTYPE html>
<html lang="zh-CN"><body style="margin:0;padding:24px;background:#f6f7f9;font-family:-apple-system,'Segoe UI',Roboto,'Helvetica Neue',Arial,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2328;">
<div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e5e7eb;border-radius:12px;padding:24px;">
  <div style="font-size:13px;color:#6b7280;margin-bottom:8px;">{{.SiteName}}</div>
  <h1 style="margin:0 0 16px;font-size:18px;line-height:1.4;">{{if .ParentAuthor}}{{.ParentAuthor}}，{{end}}{{.AuthorName}} 回复了你的评论</h1>
  <blockquote style="margin:0 0 16px;padding:12px 16px;background:#f6f7f9;border-left:3px solid #2563eb;border-radius:4px;white-space:pre-wrap;font-size:14px;line-height:1.6;">{{.Body}}</blockquote>
  {{if .PostURL}}<p style="margin:0 0 16px;"><a href="{{.PostURL}}" style="color:#2563eb;text-decoration:none;">在《{{.PostSlug}}》中查看并回复</a></p>{{end}}
  <p style="margin:0;font-size:12px;color:#9ca3af;line-height:1.6;">此邮件由 {{.SiteName}} 自动发送，请勿直接回复。</p>
</div>
</body></html>`))

// Tokens lists what a custom body may contain. The names are the ones the
// console shows, so an operator can copy them out of the page.
var Tokens = []string{
	"siteName", "siteURL", "postSlug", "postURL", "consoleURL",
	"authorName", "authorEmail", "parentAuthor", "body", "time",
}

// RenderTemplate fills a custom body with the notice.
//
// This is string replacement, not html/template, and that is deliberate on two
// counts: the body is a whole document the operator pasted (parsing it as a
// template would let a stray "{{" anywhere in their CSS break every mail), and
// replacing tokens keeps every value escaped exactly once. Unknown tokens are
// left as written, so a typo is visible in the message instead of silently
// disappearing.
func RenderTemplate(body string, n CommentNotice) string {
	values := map[string]string{
		"siteName":     fallback(n.SiteName, "站点"),
		"siteURL":      n.SiteURL,
		"postSlug":     n.PostSlug,
		"postURL":      n.PostURL(),
		"consoleURL":   n.ConsoleURL,
		"authorName":   n.AuthorName,
		"authorEmail":  n.AuthorEmail,
		"parentAuthor": n.ParentAuthor,
		"body":         n.Body,
		"time":         formatTime(n.CreatedAt),
	}

	out := body
	for _, token := range Tokens {
		out = strings.ReplaceAll(out, "{{"+token+"}}", html.EscapeString(values[token]))
	}
	return out
}

// WithCustomBody swaps the built-in HTML for the operator's, when there is one.
//
// The plain-text part is kept: it is the fallback for a client that shows no
// HTML at all, and it describes the same comment. A body that renders to
// nothing (an empty setting, a template that is only tokens) falls back to the
// built-in message rather than sending a blank one.
func WithCustomBody(m Message, body string, n CommentNotice) Message {
	if strings.TrimSpace(body) == "" {
		return m
	}
	rendered := RenderTemplate(body, n)
	if strings.TrimSpace(rendered) == "" {
		return m
	}
	m.HTML = rendered
	return m
}

// formatTime renders the comment time for a template, in local time.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}
