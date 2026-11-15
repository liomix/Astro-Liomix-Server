package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

// fakeModeration is the comment module as the console sees it: a few rows and a
// status that can change. It is in memory because the console's job is to call
// the port correctly, not to re-test the comment module's storage.
type fakeModeration struct {
	mu      sync.Mutex
	items   []ModerationComment
	deleted []string
}

var _ ModerationQueue = (*fakeModeration)(nil)

func (f *fakeModeration) List(_ context.Context, q ModerationQuery) (ModerationPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	items := make([]ModerationComment, 0)
	for _, item := range f.items {
		if q.Status != "" && item.Status != q.Status {
			continue
		}
		items = append(items, item)
	}
	return ModerationPage{Items: items}, nil
}

func (f *fakeModeration) SetStatus(_ context.Context, commentID, status string) (ModerationComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i := range f.items {
		if f.items[i].ID == commentID {
			f.items[i].Status = status
			return f.items[i], nil
		}
	}
	return ModerationComment{}, apperr.ErrNotFound
}

func (f *fakeModeration) Delete(_ context.Context, commentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted = append(f.deleted, commentID)
	return nil
}

func (f *fakeModeration) statusOf(commentID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.items {
		if item.ID == commentID {
			return item.Status
		}
	}
	return ""
}

// console is a running console with a browser-like cookie jar.
type console struct {
	server *httptest.Server
	client *http.Client
	jar    http.CookieJar
	env    *testEnv
	queue  *fakeModeration
}

// newConsole starts the console on a real HTTP server, so cookies, redirects and
// form encoding behave the way a browser will make them behave.
func newConsole(t *testing.T) *console {
	t.Helper()

	env := newTestEnv(t)
	if _, err := env.svc.Bootstrap(t.Context(), "owner", "Owner", testPassword); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	queue := &fakeModeration{items: []ModerationComment{
		{
			ID:          "c-1",
			PostSlug:    "hello",
			AuthorName:  "Ada",
			AuthorEmail: "ada@example.com",
			Body:        `nice <script>alert(1)</script>`,
			Status:      "pending",
			CreatedAt:   env.clock.Now(),
		},
	}}

	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		RegisterRoutes(mux, discardLogger(), env.svc, HTTPConfig{})
		RegisterWebUI(mux, discardLogger(), env.svc, WebUIConfig{
			Moderation: queue,
			Auditor:    env.trail,
		})
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	return &console{
		server: server,
		client: &http.Client{Jar: jar, Timeout: 5 * time.Second},
		jar:    jar,
		env:    env,
		queue:  queue,
	}
}

// noRedirectClient returns a client that stops at the first response, so a test
// can assert the status code and Set-Cookie of a redirect.
func (c *console) noRedirectClient() *http.Client {
	return &http.Client{
		Jar:     c.jar,
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// get fetches a page and returns the body.
func (c *console) get(t *testing.T, path string) (int, string) {
	t.Helper()

	resp, err := c.client.Get(c.server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

// cookies returns the jar's cookies for the console path.
func (c *console) cookies(t *testing.T) []*http.Cookie {
	t.Helper()

	parsed, err := url.Parse(c.server.URL + "/admin/")
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	return c.jar.Cookies(parsed)
}

// csrf returns the CSRF token the server put in the jar.
func (c *console) csrf(t *testing.T) string {
	t.Helper()

	for _, cookie := range c.cookies(t) {
		if cookie.Name == csrfCookieName {
			return cookie.Value
		}
	}
	t.Fatal("no csrf cookie in the jar; the page did not render a form")
	return ""
}

// sessionToken returns the session cookie value, or "" when there is none.
func (c *console) sessionToken(t *testing.T) string {
	t.Helper()

	for _, cookie := range c.cookies(t) {
		if cookie.Name == sessionCookieName {
			return cookie.Value
		}
	}
	return ""
}

// post submits a form and returns the status and Location.
func (c *console) post(t *testing.T, path string, form url.Values) (int, string, string) {
	t.Helper()

	client := c.noRedirectClient()
	resp, err := client.PostForm(c.server.URL+path, form)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read POST %s: %v", path, err)
	}
	return resp.StatusCode, resp.Header.Get("Location"), string(body)
}

// login signs in as the owner with the fixture password.
func (c *console) login(t *testing.T) {
	t.Helper()
	c.loginAs(t, "owner", testPassword)
}

// loginAs signs in through the form and fails the test when it cannot.
func (c *console) loginAs(t *testing.T, username, password string) {
	t.Helper()

	if status, body := c.get(t, "/admin/login"); status != http.StatusOK {
		t.Fatalf("login page = %d (body: %s)", status, body)
	}
	status, location, body := c.post(t, "/admin/login", url.Values{
		"csrf":     {c.csrf(t)},
		"username": {username},
		"password": {password},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("login POST = %d, want 303 (body: %s)", status, body)
	}
	if location != "/admin/comments" {
		t.Errorf("login Location = %q, want /admin/comments", location)
	}
	if c.sessionToken(t) == "" {
		t.Fatal("login did not set a session cookie")
	}
}

func TestConsoleRequiresLogin(t *testing.T) {
	t.Parallel()
	c := newConsole(t)

	status, body := c.get(t, "/admin/comments")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 after following the redirect", status)
	}
	if !strings.Contains(body, "管理员登录") {
		t.Errorf("body does not look like the login page: %s", body)
	}
	if !strings.Contains(body, `name="csrf"`) {
		t.Error("login form has no csrf field")
	}
}

func TestConsoleLoginSetsHttpOnlyCookie(t *testing.T) {
	t.Parallel()
	c := newConsole(t)

	c.get(t, "/admin/login")
	client := c.noRedirectClient()
	resp, err := client.PostForm(c.server.URL+"/admin/login", url.Values{
		"csrf":     {c.csrf(t)},
		"username": {"owner"},
		"password": {testPassword},
	})
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	var sessionCookie *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("login response has no session cookie")
	}
	if !sessionCookie.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", sessionCookie.SameSite)
	}
	if sessionCookie.Path != "/admin" {
		t.Errorf("Path = %q, want /admin", sessionCookie.Path)
	}
}

func TestConsoleLoginRejectsBadCredentials(t *testing.T) {
	t.Parallel()
	c := newConsole(t)

	c.get(t, "/admin/login")
	status, _, body := c.post(t, "/admin/login", url.Values{
		"csrf":     {c.csrf(t)},
		"username": {"owner"},
		"password": {"wrong-password-123"},
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	if !strings.Contains(body, "用户名或密码不正确") {
		t.Errorf("body does not carry the failure message: %s", body)
	}
	if c.sessionToken(t) != "" {
		t.Error("a failed login set a session cookie")
	}
}

func TestConsoleRejectsMissingCSRF(t *testing.T) {
	t.Parallel()
	c := newConsole(t)

	c.get(t, "/admin/login")
	status, _, _ := c.post(t, "/admin/login", url.Values{
		"username": {"owner"},
		"password": {testPassword},
	})
	if status != http.StatusForbidden {
		t.Fatalf("login without csrf = %d, want 403", status)
	}
	if c.sessionToken(t) != "" {
		t.Error("a request without CSRF obtained a session")
	}
}

func TestConsoleCommentsPageRendersAndEscapes(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, body := c.get(t, "/admin/comments")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "Ada") || !strings.Contains(body, "hello") {
		t.Error("page does not show the pending comment")
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("comment body was rendered unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("comment body is missing entirely; the escape assertion proves nothing")
	}
	if !strings.Contains(body, "待审") {
		t.Error("page does not label the moderation state")
	}
}

func TestConsoleModerateComment(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, location, body := c.post(t, "/admin/comments/c-1/status", url.Values{
		"csrf":   {c.csrf(t)},
		"status": {"approved"},
		"return": {"pending"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body: %s)", status, body)
	}
	if !strings.HasPrefix(location, "/admin/comments?status=pending") {
		t.Errorf("Location = %q, want the pending tab", location)
	}
	if got := c.queue.statusOf("c-1"); got != "approved" {
		t.Errorf("comment status = %q, want approved", got)
	}
	events := c.env.trail.eventsByAction("comment.moderate")
	if len(events) != 1 {
		t.Fatalf("moderation events = %d, want 1", len(events))
	}
	if events[0].Actor.Name != "owner" || events[0].ResourceID != "c-1" {
		t.Errorf("event = %+v, want the operator and the comment", events[0])
	}
}

func TestConsoleModerateRejectsUnknownStatus(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, location, _ := c.post(t, "/admin/comments/c-1/status", url.Values{
		"csrf":   {c.csrf(t)},
		"status": {"whatever"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", status)
	}
	if !strings.Contains(location, "err=bad_request") {
		t.Errorf("Location = %q, want the bad_request code", location)
	}
	if got := c.queue.statusOf("c-1"); got != "pending" {
		t.Errorf("status changed to %q despite an invalid value", got)
	}
}

func TestConsoleModerateWithoutCSRFIsRefused(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, location, _ := c.post(t, "/admin/comments/c-1/status", url.Values{
		"status": {"approved"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", status)
	}
	if !strings.Contains(location, "err=csrf") {
		t.Errorf("Location = %q, want the csrf code", location)
	}
	if got := c.queue.statusOf("c-1"); got != "pending" {
		t.Errorf("status changed to %q without a CSRF token", got)
	}
}

func TestConsoleAccountsPageAndCreate(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, body := c.get(t, "/admin/accounts")
	if status != http.StatusOK {
		t.Fatalf("accounts page = %d, want 200", status)
	}
	if !strings.Contains(body, "owner") {
		t.Error("accounts page does not list the signed-in account")
	}

	status, location, body := c.post(t, "/admin/accounts", url.Values{
		"csrf":        {c.csrf(t)},
		"username":    {"grace"},
		"displayName": {"Grace Hopper"},
		"password":    {testPassword},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303 (body: %s)", status, body)
	}
	if location != "/admin/accounts?msg=created" {
		t.Errorf("Location = %q, want the created notice", location)
	}

	accounts, _, err := c.env.svc.ListAccounts(t.Context(), AccountQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	found := false
	for _, account := range accounts {
		if account.Username == "grace" {
			found = true
		}
	}
	if !found {
		t.Error("the created account is not in the repository")
	}
	if events := c.env.trail.eventsByAction(actionAccountCreate); len(events) != 1 {
		t.Errorf("account.create events = %d, want 1", len(events))
	}
}

func TestConsoleAccountCreateReportsConflict(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, location, _ := c.post(t, "/admin/accounts", url.Values{
		"csrf":     {c.csrf(t)},
		"username": {"owner"},
		"password": {testPassword},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", status)
	}
	if !strings.Contains(location, "err=conflict_account") {
		t.Errorf("Location = %q, want the conflict code", location)
	}
}

func TestConsoleAuditPage(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, body := c.get(t, "/admin/audit")
	if status != http.StatusOK {
		t.Fatalf("audit page = %d, want 200", status)
	}
	if !strings.Contains(body, "auth.login") {
		t.Error("audit page does not show the sign-in event")
	}

	status, body = c.get(t, "/admin/audit?action=account.bootstrap")
	if status != http.StatusOK {
		t.Fatalf("filtered audit page = %d, want 200", status)
	}
	if !strings.Contains(body, "account.bootstrap") {
		t.Error("filtered audit page does not show the bootstrap event")
	}
}

func TestConsoleLogout(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, location, _ := c.post(t, "/admin/logout", url.Values{"csrf": {c.csrf(t)}})
	if status != http.StatusSeeOther {
		t.Fatalf("logout status = %d, want 303", status)
	}
	if location != "/admin/login?msg=logged_out" {
		t.Errorf("Location = %q, want the login page", location)
	}
	if c.sessionToken(t) != "" {
		t.Error("logout left the session cookie in the jar")
	}

	status, body := c.get(t, "/admin/comments")
	if status != http.StatusOK || !strings.Contains(body, "管理员登录") {
		t.Error("a signed-out visitor can still reach the queue")
	}
}

func TestConsoleUnknownPageIsProblemJSON(t *testing.T) {
	t.Parallel()
	c := newConsole(t)

	// The console only claims /admin/*; anything else still goes through the
	// API's fallback, which answers problem+json rather than HTML.
	resp, err := c.client.Get(c.server.URL + "/admin/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want problem+json", ct)
	}
}

func TestConsoleSelfPasswordChangeSignsOut(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	accounts, _, err := c.env.svc.ListAccounts(t.Context(), AccountQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	var self *Account
	for _, account := range accounts {
		if account.Username == "owner" {
			self = account
		}
	}
	if self == nil {
		t.Fatal("owner account not found")
	}

	status, location, _ := c.post(t, "/admin/accounts/"+self.ID+"/password", url.Values{
		"csrf":            {c.csrf(t)},
		"currentPassword": {testPassword},
		"password":        {"another-long-password"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", status)
	}
	if !strings.Contains(location, "/admin/login") {
		t.Errorf("Location = %q, want the login page after revoking one's own sessions", location)
	}
	if c.sessionToken(t) != "" {
		t.Error("the session cookie survived a self password change")
	}
}

func TestConsolePasswordChangeRequiresCurrentForSelf(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	accounts, _, err := c.env.svc.ListAccounts(t.Context(), AccountQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	var self *Account
	for _, account := range accounts {
		if account.Username == "owner" {
			self = account
		}
	}

	status, location, _ := c.post(t, "/admin/accounts/"+self.ID+"/password", url.Values{
		"csrf":     {c.csrf(t)},
		"password": {"another-long-password"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", status)
	}
	if !strings.Contains(location, "err=bad_current") {
		t.Errorf("Location = %q, want the bad_current code", location)
	}
	if c.sessionToken(t) == "" {
		t.Error("a rejected password change signed the operator out")
	}
}

func TestConsoleRequiresModerationForCommentPages(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	if _, err := env.svc.Bootstrap(t.Context(), "owner", "Owner", testPassword); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		RegisterWebUI(mux, discardLogger(), env.svc, WebUIConfig{})
	})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/admin/comments")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when no moderation queue is wired", resp.StatusCode)
	}
}

// TestConsoleShowsInitialPasswordReminder is the first-run nudge: an account
// created by the CLI still holds a generated password, and the console says so
// above every page until it is changed.
func TestConsoleShowsInitialPasswordReminder(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	for _, path := range []string{"/admin/comments", "/admin/accounts", "/admin/audit"} {
		status, body := c.get(t, path)
		if status != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, status)
		}
		if !strings.Contains(body, "仍在使用初始密码") {
			t.Errorf("%s does not carry the initial-password reminder", path)
		}
		if !strings.Contains(body, "去修改") {
			t.Errorf("%s reminder has no link to the account page", path)
		}
		// The reminder is a toast fixed to the top centre of the viewport, not
		// an inline banner inside the page body.
		if !strings.Contains(body, `class="console-toasts"`) {
			t.Errorf("%s has no toast container", path)
		}
	}
}

func TestConsoleReminderClearsAfterPasswordChange(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	accounts, _, err := c.env.svc.ListAccounts(t.Context(), AccountQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	var self *Account
	for _, account := range accounts {
		if account.Username == "owner" {
			self = account
		}
	}
	if self == nil {
		t.Fatal("owner account not found")
	}

	status, _, body := c.post(t, "/admin/accounts/"+self.ID+"/password", url.Values{
		"csrf":            {c.csrf(t)},
		"currentPassword": {testPassword},
		"password":        {"another-long-password"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("password change = %d, want 303 (body: %s)", status, body)
	}

	c.loginAs(t, "owner", "another-long-password")
	status, body = c.get(t, "/admin/comments")
	if status != http.StatusOK {
		t.Fatalf("comments page = %d, want 200", status)
	}
	if strings.Contains(body, "仍在使用初始密码") {
		t.Error("the reminder survived a password change")
	}
}

func TestConsoleLoginHintsWhenNoAccountsExist(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		RegisterWebUI(mux, discardLogger(), env.svc, WebUIConfig{})
	})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/admin/login")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "还没有管理员账号") {
		t.Error("the login page does not tell a new operator that no account exists")
	}
	if !strings.Contains(string(body), "server admin create") {
		t.Error("the hint does not name the command that creates the first account")
	}
}

func TestConsoleLoginPageShowsPostChangeNotice(t *testing.T) {
	t.Parallel()
	c := newConsole(t)

	// A self password change lands back on the login page with a code; the page
	// has to render it, or the operator is signed out with no explanation.
	status, body := c.get(t, "/admin/login?msg=password_changed")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "密码已更新") {
		t.Error("the login page does not render the password-changed notice")
	}
}

func TestConsoleLoginPageExplainsWhereCredentialsComeFrom(t *testing.T) {
	t.Parallel()
	c := newConsole(t)

	status, body := c.get(t, "/admin/login")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "首次登录") {
		t.Error("the login page does not explain where the first credential comes from")
	}
	if !strings.Contains(body, "server admin create") {
		t.Error("the tip does not name the command that creates the account")
	}
	// The page is unauthenticated: it must not name an account or echo a
	// password, even though it knows accounts exist.
	if strings.Contains(body, testPassword) || strings.Contains(body, "owner") {
		t.Error("the login page leaks an account name or password")
	}
}

func TestConsoleLoginShowsInitialCredentialsInDevelopment(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	if _, err := env.svc.Bootstrap(t.Context(), "owner", "Owner", testPassword); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		RegisterWebUI(mux, discardLogger(), env.svc, WebUIConfig{
			ShowInitialCredentials: true,
		})
	})
	server := httptest.NewServer(router)
	defer server.Close()

	body := getBody(t, server.URL+"/admin/login")
	if !strings.Contains(body, "仍在使用初始密码") {
		t.Error("the login page does not say the password is still initial")
	}
	if !strings.Contains(body, "owner") || !strings.Contains(body, testPassword) {
		t.Error("the login page does not show the initial account and password")
	}

	// Changing the password retires both the plaintext copy and the display.
	accounts, _, err := env.svc.ListAccounts(t.Context(), AccountQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	if err := env.svc.ChangePassword(t.Context(), accounts[0].ID, ChangePasswordInput{
		CurrentPassword: testPassword,
		NewPassword:     "another-long-password",
	}, audit.Actor{ID: accounts[0].ID, Name: accounts[0].Username}); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}

	body = getBody(t, server.URL+"/admin/login")
	if strings.Contains(body, testPassword) {
		t.Error("the login page still shows the retired password")
	}
	if !strings.Contains(body, "首次登录") {
		t.Error("the login page does not fall back to the generic hint")
	}
}

// getBody fetches a URL and returns its body.
func getBody(t *testing.T, url string) string {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}

func TestRelativeTimeAndFirstRune(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"never", time.Time{}, "从未"},
		{"just now", now.Add(-30 * time.Second), "刚刚"},
		{"minutes", now.Add(-5 * time.Minute), "5 分钟前"},
		{"hours", now.Add(-2 * time.Hour), "2 小时前"},
		{"days", now.Add(-72 * time.Hour), "3 天前"},
	}
	for _, tc := range cases {
		if got := relativeTime(tc.at); got != tc.want {
			t.Errorf("relativeTime(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}

	for input, want := range map[string]string{"Ada": "A", " 张 三": "张", "": "?"} {
		if got := firstRune(input); got != want {
			t.Errorf("firstRune(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestConsoleServesEmbeddedAssets(t *testing.T) {
	t.Parallel()
	c := newConsole(t)

	cases := []struct {
		path   string
		ctype  string
		marker string
	}{
		{"/admin/assets/bootstrap.min.css", "text/css", "Bootstrap  v5.3.9"},
		{"/admin/assets/app.css", "text/css", "--console-primary"},
		{"/admin/assets/app.js", "text/javascript", "admin_snooze_initial_password"},
		{"/admin/assets/theme.js", "text/javascript", "prefers-color-scheme: dark"},
	}
	for _, tc := range cases {
		resp, err := c.client.Get(c.server.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d, want 200", tc.path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, tc.ctype) {
			t.Errorf("%s Content-Type = %q, want %s", tc.path, ct, tc.ctype)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("%s Cache-Control = %q, want an immutable policy", tc.path, cc)
		}
		if !strings.Contains(string(body), tc.marker) {
			t.Errorf("%s does not contain %q", tc.path, tc.marker)
		}

		etag := resp.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("%s has no ETag", tc.path)
		}
		req, err := http.NewRequest(http.MethodGet, c.server.URL+tc.path, nil)
		if err != nil {
			t.Fatalf("build conditional request: %v", err)
		}
		req.Header.Set("If-None-Match", etag)
		conditional, err := c.client.Do(req)
		if err != nil {
			t.Fatalf("conditional GET %s: %v", tc.path, err)
		}
		conditional.Body.Close()
		if conditional.StatusCode != http.StatusNotModified {
			t.Errorf("%s conditional = %d, want 304", tc.path, conditional.StatusCode)
		}
	}
}

func TestConsoleCSPUsesSelfHostedAssets(t *testing.T) {
	t.Parallel()
	c := newConsole(t)

	resp, err := c.client.Get(c.server.URL + "/admin/login")
	if err != nil {
		t.Fatalf("GET login: %v", err)
	}
	defer resp.Body.Close()

	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "style-src 'self'") {
		t.Errorf("CSP = %q, want self-hosted scripts and styles", csp)
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP still allows inline code: %q", csp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read login: %v", err)
	}
	if !strings.Contains(string(body), `/admin/assets/bootstrap.min.css?v=`) {
		t.Error("the page does not link the vendored Bootstrap stylesheet")
	}
	if !strings.Contains(string(body), `/admin/assets/app.css?v=`) {
		t.Error("the page does not link the versioned theme stylesheet")
	}
	if !strings.Contains(string(body), `/admin/assets/app.js?v=`) {
		t.Error("the page does not link the versioned script")
	}
}

func TestConsoleReminderSnoozesForTenMinutes(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	if _, body := c.get(t, "/admin/comments"); !strings.Contains(body, "仍在使用初始密码") {
		t.Fatal("the reminder is missing before it is dismissed")
	}

	parsed, err := url.Parse(c.server.URL + "/admin/")
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	c.jar.SetCookies(parsed, []*http.Cookie{{Name: snoozeCookieName, Value: "1", Path: "/admin"}})
	if _, body := c.get(t, "/admin/comments"); strings.Contains(body, "仍在使用初始密码") {
		t.Error("the reminder ignored the snooze cookie")
	}

	// Clearing the cookie brings it back, which is what happens when the
	// ten-minute window ends.
	c.jar.SetCookies(parsed, []*http.Cookie{{Name: snoozeCookieName, Value: "", Path: "/admin", MaxAge: -1}})
	if _, body := c.get(t, "/admin/comments"); !strings.Contains(body, "仍在使用初始密码") {
		t.Error("the reminder did not return after the snooze was cleared")
	}
}

func TestConsoleLoginCredentialsRespectSnooze(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	if _, err := env.svc.Bootstrap(t.Context(), "owner", "Owner", testPassword); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		RegisterWebUI(mux, discardLogger(), env.svc, WebUIConfig{ShowInitialCredentials: true})
	})
	server := httptest.NewServer(router)
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	parsed, err := url.Parse(server.URL + "/admin/")
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	body := getBody(t, server.URL+"/admin/login")
	if !strings.Contains(body, testPassword) {
		t.Fatal("the login page does not show the initial password")
	}

	jar.SetCookies(parsed, []*http.Cookie{{Name: snoozeCookieName, Value: "1", Path: "/admin"}})
	resp, err := client.Get(server.URL + "/admin/login")
	if err != nil {
		t.Fatalf("GET login with snooze: %v", err)
	}
	defer resp.Body.Close()
	dismissed, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read login: %v", err)
	}
	if strings.Contains(string(dismissed), testPassword) {
		t.Error("the snoozed login page still shows the password")
	}
	if strings.Contains(string(dismissed), "首次登录") {
		t.Error("a differently worded nudge replaced the dismissed reminder")
	}
}
