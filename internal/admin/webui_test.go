package admin

import (
	"context"
	"fmt"
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

func (f *fakeModeration) Counts(_ context.Context, q ModerationQuery) (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	counts := map[string]int{"pending": 0, "approved": 0, "rejected": 0, "spam": 0}
	for _, item := range f.items {
		counts[item.Status]++
	}
	return counts, nil
}

func (f *fakeModeration) CountSince(_ context.Context, since time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	total := 0
	for _, item := range f.items {
		if !item.CreatedAt.Before(since) {
			total++
		}
	}
	return total, nil
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

func (f *fakeModeration) BulkSetStatus(_ context.Context, commentIDs []string, status string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	affected := 0
	for i := range f.items {
		for _, id := range commentIDs {
			if f.items[i].ID == id {
				f.items[i].Status = status
				affected++
			}
		}
	}
	return affected, nil
}

func (f *fakeModeration) BulkDelete(_ context.Context, commentIDs []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted = append(f.deleted, commentIDs...)
	return len(commentIDs), nil
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
	if location != "/admin" {
		t.Errorf("login Location = %q, want the dashboard", location)
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
	// The tab survives the redirect; url.Values sorts the parameters, so the
	// check is on the parsed query rather than on the raw string.
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if got := parsed.Query().Get("status"); got != "pending" {
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

func TestConsoleProfilePageAndSave(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	// The settings are a dialog on every page, not a page of their own.
	status, body := c.get(t, "/admin")
	if status != http.StatusOK {
		t.Fatalf("dashboard = %d, want 200", status)
	}
	if !strings.Contains(body, `id="profile-dialog"`) {
		t.Error("the dashboard does not carry the settings dialog")
	}
	if strings.Contains(body, "新建账号") {
		t.Error("the console still offers to create accounts")
	}
	// The account's login name is not shown next to the avatar.
	if strings.Contains(body, "@owner") {
		t.Error("the avatar menu still shows the @username label")
	}

	status, location, body := c.post(t, "/admin/profile", url.Values{
		"csrf":        {c.csrf(t)},
		"displayName": {"Ada L."},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("profile save = %d, want 303 (body: %s)", status, body)
	}
	if location != "/admin?msg=profile_saved" {
		t.Errorf("Location = %q, want the dashboard with the saved notice", location)
	}

	accounts, _, err := c.env.svc.ListAccounts(t.Context(), AccountQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	var updated *Account
	for _, account := range accounts {
		if account.Username == "owner" {
			updated = account
		}
	}
	if updated == nil {
		t.Fatal("owner account not found")
	}
	if updated.DisplayName != "Ada L." {
		t.Errorf("display name = %q, want the saved value", updated.DisplayName)
	}
	if events := c.env.trail.eventsByAction(actionAccountUpdate); len(events) != 1 {
		t.Errorf("account.update events = %d, want 1", len(events))
	}
}

func TestConsoleLegacyAccountsPathRedirects(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	resp, err := c.noRedirectClient().Get(c.server.URL + "/admin/accounts")
	if err != nil {
		t.Fatalf("GET /admin/accounts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/admin" {
		t.Errorf("Location = %q, want /admin", got)
	}
}

func TestConsoleSelfPasswordChangeSignsOut(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, location, _ := c.post(t, "/admin/profile/password", url.Values{
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

	status, location, _ := c.post(t, "/admin/profile/password", url.Values{
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

	for _, path := range []string{"/admin", "/admin/comments", "/admin/profile", "/admin/audit"} {
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

	status, _, body := c.post(t, "/admin/profile/password", url.Values{
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

func TestConsoleBulkModeration(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, location, body := c.post(t, "/admin/comments/bulk", url.Values{
		"csrf":   {c.csrf(t)},
		"return": {"pending"},
		"action": {"approve"},
		"ids":    {"c-1"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("bulk approve = %d, want 303 (body: %s)", status, body)
	}
	if !strings.Contains(location, "msg=bulk_done") {
		t.Errorf("Location = %q, want the bulk confirmation", location)
	}
	if got := c.queue.statusOf("c-1"); got != "approved" {
		t.Errorf("comment c-1 status = %q, want approved", got)
	}

	// The action is recorded once for the batch, with what was asked and what
	// actually changed — an auditor needs both numbers.
	events := c.env.trail.eventsByAction("comment.bulk.approve")
	if len(events) != 1 {
		t.Fatalf("bulk audit events = %d, want 1", len(events))
	}
	if got := fmt.Sprint(events[0].Detail["requested"]); got != "1" {
		t.Errorf("detail.requested = %v, want 1", events[0].Detail["requested"])
	}
}

func TestConsoleBulkRejectsEmptySelection(t *testing.T) {
	t.Parallel()
	c := newConsole(t)
	c.login(t)

	status, location, body := c.post(t, "/admin/comments/bulk", url.Values{
		"csrf":   {c.csrf(t)},
		"action": {"approve"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("bulk with no ids = %d, want 303 (body: %s)", status, body)
	}
	if !strings.Contains(location, "err=no_selection") {
		t.Errorf("Location = %q, want the empty-selection code", location)
	}
	if got := c.queue.statusOf("c-1"); got != "pending" {
		t.Errorf("comment c-1 status = %q, want it untouched", got)
	}
}

// fakeMail is the mail subsystem as the console sees it, without SMTP.
type fakeMail struct {
	mu      sync.Mutex
	enabled bool
	sent    []string
}

func (f *fakeMail) Info() MailInfo {
	return MailInfo{Enabled: f.enabled, Host: "smtp.example.com", Port: 587, TLS: "starttls",
		From: "blog@example.com", Admin: "owner@example.com"}
}

func (f *fakeMail) SendTest(_ context.Context, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, to)
	return nil
}

func (f *fakeMail) sentTo() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	copy(out, f.sent)
	return out
}

func TestConsoleMailPageAndSwitches(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	if _, err := env.svc.Bootstrap(t.Context(), "owner", "Owner", testPassword); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	mailer := &fakeMail{enabled: true}
	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		RegisterWebUI(mux, discardLogger(), env.svc, WebUIConfig{
			Auditor: env.trail,
			Mail:    mailer,
		})
	})
	server := httptest.NewServer(router)
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	c := &console{
		server: server,
		client: &http.Client{Jar: jar, Timeout: 5 * time.Second},
		jar:    jar,
		env:    env,
	}
	c.login(t)

	status, body := c.get(t, "/admin/mail")
	if status != http.StatusOK {
		t.Fatalf("mail page = %d, want 200", status)
	}
	if !strings.Contains(body, "通知开关") || !strings.Contains(body, "smtp.example.com") {
		t.Error("the mail page does not show the switches and the SMTP configuration")
	}
	if !strings.Contains(body, "邮件管理") {
		t.Error("the nav does not offer the mail page")
	}

	status, location, body := c.post(t, "/admin/mail", url.Values{
		"csrf":        {c.csrf(t)},
		"replyNotify": {"on"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("mail save = %d, want 303 (body: %s)", status, body)
	}
	if location != "/admin/mail?msg=mail_saved" {
		t.Errorf("Location = %q, want the saved notice", location)
	}
	prefs, err := env.svc.MailPreferences(t.Context())
	if err != nil {
		t.Fatalf("MailPreferences() error = %v", err)
	}
	if !prefs.ReplyNotify || prefs.AdminNotify {
		t.Errorf("prefs = %+v, want only the reply switch on", prefs)
	}
	// Turning a switch on or off is a privileged action and is recorded.
	if events := env.trail.eventsByAction("settings.mail"); len(events) != 1 {
		t.Errorf("settings.mail events = %d, want 1", len(events))
	}

	status, location, _ = c.post(t, "/admin/mail/test", url.Values{
		"csrf": {c.csrf(t)},
		"to":   {"reader@example.com"},
	})
	if status != http.StatusSeeOther || !strings.Contains(location, "mail_test_sent") {
		t.Errorf("test send = %d %q, want a redirect to the sent notice", status, location)
	}
	if got := mailer.sentTo(); len(got) != 1 || got[0] != "reader@example.com" {
		t.Errorf("sent = %v, want one message to the requested address", got)
	}
}

func TestConsoleMailPageWhenSMTPServerIsOff(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	if _, err := env.svc.Bootstrap(t.Context(), "owner", "Owner", testPassword); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		RegisterWebUI(mux, discardLogger(), env.svc, WebUIConfig{
			Auditor: env.trail,
			Mail:    &fakeMail{},
		})
	})
	server := httptest.NewServer(router)
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	c := &console{
		server: server,
		client: &http.Client{Jar: jar, Timeout: 5 * time.Second},
		jar:    jar,
		env:    env,
	}
	c.login(t)

	status, body := c.get(t, "/admin/mail")
	if status != http.StatusOK {
		t.Fatalf("mail page = %d, want 200", status)
	}
	if !strings.Contains(body, "SMTP 未配置") {
		t.Error("the page does not say that SMTP is off")
	}
	if strings.Contains(body, `type="submit" >发送测试`) {
		t.Error("the test button is enabled without an SMTP server")
	}
}
