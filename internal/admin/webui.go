package admin

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
)

// The console is server-rendered with html/template and no build step: the
// templates are embedded in the binary, the CSS lives in the layout, and the
// only script is an inline confirm() on destructive buttons. That is deliberate
// — this service is a JSON API, and adding a Node toolchain for four pages would
// cost more than the pages are worth.
//
//go:embed webui_templates/*.html
var webUITemplates embed.FS

//go:embed webui_assets/css/app.css webui_assets/js/app.js webui_assets/js/theme.js
var webUIAssets embed.FS

// Console assets are embedded and served with an immutable cache policy: the
// URL carries a content hash, so a deploy that changes the CSS changes the URL
// and no browser can pair new markup with old styles.
var (
	consoleCSS          = mustAsset("webui_assets/css/app.css")
	consoleJS           = mustAsset("webui_assets/js/app.js")
	consoleThemeJS      = mustAsset("webui_assets/js/theme.js")
	consoleAssetVersion = assetHash(consoleCSS, consoleJS, consoleThemeJS)
)

// Cookie and form names used by the console. They are unexported because no
// other package should depend on the wire details of these pages.
const (
	sessionCookieName = "admin_session"
	csrfCookieName    = "admin_csrf"
	csrfFormField     = "csrf"
	// minOwnerKeyLength refuses a key short enough to guess: a weak key gives the
	// owner the false comfort of thinking the badge is protected.
	minOwnerKeyLength = 16
	// snoozeCookieName suppresses the initial-password reminder after the
	// operator dismisses it. It is not a credential and not HttpOnly: the page
	// script sets it when the toast is closed.
	snoozeCookieName = "admin_snooze_initial_password"
	// csrfCookieTTL is how long a rendered form stays submittable. It only
	// guards against cross-site posts; it is not a second session.
	csrfCookieTTL = 12 * time.Hour
	// webUIMaxBodyBytes caps a form post. Every form here carries a handful of
	// short fields.
	webUIMaxBodyBytes int64 = 16 << 10
	// webUIPageSize is how many rows one page shows. The API still pages with a
	// cursor; the console just makes the next link available.
	webUIPageSize = 50
	// maxSearchKeyword bounds the moderation search box, matching the comment
	// module's own limit. The console truncates; the module refuses. Two layers
	// because the URL is user-editable and the handler must not trust it.
	maxSearchKeyword = 100
)

// moderationStatuses is the comment states the console can list and set. It
// mirrors the comment module's enum; an unknown value is refused before it
// reaches the adapter.
var moderationStatuses = []string{"pending", "approved", "rejected", "spam"}

// WebUIConfig carries what the console needs from the rest of the service.
type WebUIConfig struct {
	// Moderation is the comment module seen through the console's port. When
	// nil the comment pages are not mounted, which is what a build without a
	// comment module should do rather than serving a page that always errors.
	Moderation ModerationQueue
	// Auditor records moderation actions. Account actions are recorded by the
	// service itself; the comment module has no admin service, so the console
	// is where those events are written.
	Auditor audit.Recorder
	// SecureCookies marks both cookies Secure. Enable it in production, where
	// the console is served over HTTPS; leaving it off locally is what lets
	// http://localhost work.
	SecureCookies bool
	// ShowInitialCredentials prints the initial account and password on the
	// login page while the account still holds them. It is a development
	// convenience with a real cost: the login page is unauthenticated, so
	// anyone who can reach it can read the credential. cmd/server enables it
	// only when APP_ENV is development.
	ShowInitialCredentials bool
	// PreferredInitialUsername is shown ahead of other unclaimed accounts, so
	// the seeded default administrator keeps appearing after more accounts are
	// added. Empty falls back to "only if exactly one account qualifies".
	PreferredInitialUsername string
	// Mail powers the mail page. When nil the nav item is hidden and the route
	// answers 404, which is what a build without the mail package should do.
	Mail MailService
}

// pageData is the view model every page shares. One struct rather than one per
// page keeps the layout's nav and notices identical everywhere; unused fields
// are simply zero and never rendered.
type pageData struct {
	Title   string
	Nav     string
	Account *Account
	// CSRFToken is rendered into every form as a hidden field. It is read from
	// an HttpOnly cookie, so page scripts cannot exfiltrate it either.
	CSRFToken string
	Flash     string
	Error     string
	// HasModeration hides the comment tab when no queue is wired.
	HasModeration bool
	// HasMail shows the mail tab. It follows whether the console was given a
	// mail service, not whether SMTP is configured: an operator still needs
	// the page to see that it is off.
	HasMail bool
	// AutoApprove is the console's moderation policy: publish new comments
	// without review.
	AutoApprove bool
	// Mail is the outbound-mail configuration shown on the settings page.
	Mail MailSettings
	// NoAccounts is set on the login page when the account table is empty. A
	// form that always answers "wrong password" would send a new operator
	// looking for a typo instead of telling them to create the account.
	NoAccounts bool
	// LoginTip is set on the login page when accounts exist, to explain where
	// the credential comes from. It never names an account or a password: the
	// page is unauthenticated.
	LoginTip bool
	// InitialUsername and InitialPassword are filled only in development and
	// only while exactly one account still holds an initial password. They are
	// rendered on the unauthenticated login page, which is the whole point and
	// the whole risk.
	InitialUsername string
	InitialPassword string
	// AssetVersion busts the asset URL when the embedded CSS or JS changes.
	AssetVersion string

	// Greeting is the time-of-day line on the dashboard ("早上好").
	Greeting string
	// Date is the dashboard's calendar line, in the server's local zone.
	Date string
	// Today is how many comments arrived since midnight, in any state.
	Today int
	// Recent is the newest handful of pending comments, for the dashboard.
	Recent []ModerationComment

	Statuses   []string
	Status     string
	Comments   []ModerationComment
	NextCursor string
	// Keyword is the search the operator typed, echoed back into the form so
	// the box keeps its value and every action link can carry it forward.
	Keyword string
	// Counts is how many comments each state holds under the current keyword
	// filter. Nil when the count query failed; the template then shows tabs
	// without numbers rather than numbers that lie.
	Counts map[string]int

	Accounts []*Account

	Entries []*audit.Entry
	Action  string
	// Session is the console session behind the current request, shown on the
	// profile page so the operator can see when it expires.
	Session *Session
}

// webUI serves the operator console.
type webUI struct {
	svc                      *Service
	moderation               ModerationQueue
	auditor                  audit.Recorder
	logger                   *slog.Logger
	secureCookies            bool
	showInitialCredentials   bool
	preferredInitialUsername string
	mail                     MailService
}

// RegisterWebUI mounts the console on mux under /admin.
//
// These routes are intentionally separate from /api/v1/admin: the API
// authenticates with a bearer token, while the pages authenticate with an
// HttpOnly cookie and require a CSRF token. Mixing the two would make every
// JSON endpoint cookie-authenticated and therefore CSRF-relevant.
func RegisterWebUI(mux *http.ServeMux, logger *slog.Logger, svc *Service, cfg WebUIConfig) {
	if logger == nil {
		logger = slog.Default()
	}
	u := &webUI{
		svc:                      svc,
		moderation:               cfg.Moderation,
		auditor:                  cfg.Auditor,
		logger:                   logger,
		secureCookies:            cfg.SecureCookies,
		showInitialCredentials:   cfg.ShowInitialCredentials,
		preferredInitialUsername: cfg.PreferredInitialUsername,
		mail:                     cfg.Mail,
	}

	mux.HandleFunc("GET /admin/assets/app.css", u.asset(consoleCSS, "text/css; charset=utf-8"))
	mux.HandleFunc("GET /admin/assets/app.js", u.asset(consoleJS, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /admin/assets/theme.js", u.asset(consoleThemeJS, "text/javascript; charset=utf-8"))

	mux.HandleFunc("GET /admin", u.root)
	mux.HandleFunc("GET /admin/{$}", u.root)
	mux.HandleFunc("GET /admin/login", u.loginPage)
	mux.HandleFunc("POST /admin/login", u.loginSubmit)
	mux.HandleFunc("POST /admin/logout", u.withSession(u.logout))

	// The console manages exactly one account: the one signed in. Creating,
	// disabling and deleting administrators is still available over the JSON
	// API, but it is not a page — a blog with one operator does not need a user
	// management screen, and pretending otherwise invites mistakes.
	// The settings live in a dialog on every page, so there is no page of their
	// own; the two forms behind it still post here.
	mux.HandleFunc("POST /admin/profile", u.withSession(u.profileSave))
	mux.HandleFunc("POST /admin/profile/password", u.withSession(u.profilePassword))
	// Old links and bookmarks pointed here.
	mux.HandleFunc("GET /admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /admin/profile", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})

	mux.HandleFunc("GET /admin/audit", u.withSession(u.auditPage))
	mux.HandleFunc("POST /admin/audit/bulk", u.withSession(u.auditBulk))
	if cfg.Mail != nil {
		mux.HandleFunc("GET /admin/mail", u.withSession(u.mailPage))
		mux.HandleFunc("POST /admin/mail", u.withSession(u.mailSave))
		mux.HandleFunc("POST /admin/mail/test", u.withSession(u.mailTest))
	}

	if cfg.Moderation != nil {
		mux.HandleFunc("GET /admin/comments", u.withSession(u.commentsPage))
		mux.HandleFunc("POST /admin/comments/{id}/status", u.withSession(u.commentStatus))
		mux.HandleFunc("POST /admin/comments/{id}/delete", u.withSession(u.commentDelete))
		mux.HandleFunc("POST /admin/comments/bulk", u.withSession(u.commentsBulk))
		mux.HandleFunc("POST /admin/comments/policy", u.withSession(u.commentsPolicy))
	}
}

// root renders the dashboard, or the login form for a visitor.
func (u *webUI) root(w http.ResponseWriter, r *http.Request) {
	account, session, err := u.currentSession(r)
	if err != nil {
		u.redirect(w, r, "/admin/login")
		return
	}
	if u.snoozed(r) {
		account = account.Clone()
		account.PasswordInitial = false
	}
	u.dashboard(w, r, &Principal{Account: account, Session: session})
}

// dashboard is the landing page: what arrived, what is waiting, and what the
// operator last did. It deliberately has no charts — four numbers and two lists
// answer the questions a blog owner actually has.
func (u *webUI) dashboard(w http.ResponseWriter, r *http.Request, p *Principal) {
	now := time.Now()
	data := pageData{
		Title: "首页", Nav: "home", Account: p.Account,
		CSRFToken: u.csrfToken(w, r),
		Greeting:  greeting(now),
		Date:      dateLine(now),
	}
	data.Flash, data.Error = u.notices(r)

	if u.moderation != nil {
		counts, err := u.moderation.Counts(r.Context(), ModerationQuery{})
		if err != nil {
			u.logger.WarnContext(r.Context(), "dashboard counts", slog.Any("error", err))
		}
		data.Counts = counts

		startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		if today, err := u.moderation.CountSince(r.Context(), startOfDay); err != nil {
			u.logger.WarnContext(r.Context(), "dashboard today count", slog.Any("error", err))
		} else {
			data.Today = today
		}

		page, err := u.moderation.List(r.Context(), ModerationQuery{Status: "pending", Limit: 5})
		if err != nil {
			u.logger.WarnContext(r.Context(), "dashboard pending list", slog.Any("error", err))
		} else {
			data.Recent = page.Items
		}
	}

	// Only sign-ins: the dashboard feed answers "who used this console", while
	// the audit page keeps the full record of moderation.
	if entries, _, err := u.svc.ListAuditLogs(r.Context(), audit.Query{Limit: 8, Action: actionLogin}); err != nil {
		u.logger.WarnContext(r.Context(), "dashboard audit feed", slog.Any("error", err))
	} else {
		data.Entries = entries
	}

	u.render(w, http.StatusOK, "dashboard", data)
}

// currentSession resolves the session cookie without writing a response.
func (u *webUI) currentSession(r *http.Request) (*Account, *Session, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, nil, apperr.ErrUnauthorized
	}
	return u.svc.Authenticate(r.Context(), cookie.Value)
}

// withSession guards a page or form action with the console cookie.
//
// A missing or dead session redirects to the login form rather than answering
// 401: a browser told "401" shows an empty page, while a redirect lands the
// operator somewhere they can act.
func (u *webUI) withSession(next func(http.ResponseWriter, *http.Request, *Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, session, err := u.currentSession(r)
		if err != nil {
			u.clearSessionCookie(w)
			u.redirect(w, r, "/admin/login")
			return
		}
		if u.snoozed(r) {
			// The reminder was dismissed a moment ago; the account still holds
			// its initial password, but the console respects the dismissal
			// until the cookie expires. Authentication itself is unchanged.
			account = account.Clone()
			account.PasswordInitial = false
		}
		next(w, r, &Principal{Account: account, Session: session})
	}
}

// loginPage renders the form, or leaves an already signed-in operator where
// they were going.
func (u *webUI) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, _, err := u.currentSession(r); err == nil {
		u.redirect(w, r, "/admin")
		return
	}
	u.render(w, http.StatusOK, "login", u.loginData(w, r))
}

// loginData builds the login page model, including the first-run hint.
func (u *webUI) loginData(w http.ResponseWriter, r *http.Request) pageData {
	// The login page is where a sign-out and a self password change land, so it
	// has to read the same ?msg/?err codes the other pages do; otherwise the
	// redirect explains nothing.
	flash, errText := u.notices(r)
	data := pageData{Title: "登录", CSRFToken: u.csrfToken(w, r), Flash: flash, Error: errText}

	snoozed := u.snoozed(r)
	if u.showInitialCredentials && !snoozed {
		username, password, ok, err := u.svc.InitialCredential(r.Context(), u.preferredInitialUsername)
		if err != nil {
			u.logger.WarnContext(r.Context(), "resolve initial credential", slog.Any("error", err))
		} else if ok {
			data.InitialUsername = username
			data.InitialPassword = password
			return data
		}
	}

	hasAccounts, err := u.svc.HasAccounts(r.Context())
	if err != nil {
		// A failed count is not a reason to hide the form; the hint is simply
		// omitted rather than guessed at.
		u.logger.WarnContext(r.Context(), "check for administrator accounts", slog.Any("error", err))
		return data
	}
	if hasAccounts {
		// A dismissed reminder stays dismissed for ten minutes; showing a
		// differently worded nudge in its place would defeat the close button.
		data.LoginTip = !snoozed
	} else {
		data.NoAccounts = true
	}
	return data
}

// loginSubmit exchanges the form for a session cookie.
//
// The failure message is the same for an unknown account and a wrong password,
// matching the API: the console must not become the place where usernames are
// enumerated.
func (u *webUI) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !u.parseForm(w, r) {
		u.renderLoginError(w, r, http.StatusBadRequest, "表单格式不正确，请重试。")
		return
	}
	if !u.csrfOK(r) {
		u.renderLoginError(w, r, http.StatusForbidden, "表单已过期，请刷新页面后重试。")
		return
	}

	ip, userAgent := requestOrigin(r)
	_, session, token, err := u.svc.Login(r.Context(), LoginInput{
		Username:  r.PostFormValue("username"),
		Password:  r.PostFormValue("password"),
		IP:        ip,
		UserAgent: userAgent,
	})
	if err != nil {
		if errors.Is(err, apperr.ErrUnauthorized) {
			u.renderLoginError(w, r, http.StatusUnauthorized, "用户名或密码不正确。")
			return
		}
		u.logger.ErrorContext(r.Context(), "console login", slog.Any("error", err))
		u.renderLoginError(w, r, http.StatusInternalServerError, "登录失败，请稍后重试。")
		return
	}
	u.setSessionCookie(w, token, session.ExpiresAt)
	u.redirect(w, r, "/admin")
}

// renderLoginError re-renders the form with a message; the status is the one the
// browser should show in devtools, not a redirect, so the back button behaves.
func (u *webUI) renderLoginError(w http.ResponseWriter, r *http.Request, status int, message string) {
	data := u.loginData(w, r)
	data.Error = message
	u.render(w, status, "login", data)
}

// logout revokes the session behind the cookie and clears it.
func (u *webUI) logout(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !u.parseForm(w, r) || !u.csrfOK(r) {
		u.clearSessionCookie(w)
		u.redirect(w, r, "/admin/login")
		return
	}
	if err := u.svc.Logout(r.Context(), p.Session.ID, u.actor(r, p)); err != nil && !errors.Is(err, apperr.ErrNotFound) {
		u.logger.WarnContext(r.Context(), "console logout", slog.Any("error", err))
	}
	u.clearSessionCookie(w)
	u.redirect(w, r, "/admin/login?msg=logged_out")
}

// commentsPage lists one moderation state.
func (u *webUI) commentsPage(w http.ResponseWriter, r *http.Request, p *Principal) {
	if u.moderation == nil {
		u.render(w, http.StatusNotImplemented, "comments", pageData{
			Title: "评论审核", Nav: "comments", Account: p.Account,
			CSRFToken: u.csrfToken(w, r), Error: "未配置评论模块。",
		})
		return
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = "pending"
	}
	if !validModerationStatus(status) {
		u.render(w, http.StatusBadRequest, "comments", pageData{
			Title: "评论审核", Nav: "comments", Account: p.Account,
			CSRFToken: u.csrfToken(w, r), Statuses: moderationStatuses, Status: "pending",
			Error: "状态不合法。",
		})
		return
	}

	// A search is a person typing into a box, so an over-long value is cut
	// rather than refused: losing the tail of a paste is a smaller surprise
	// than an error page. The comment module bounds it again on its side.
	keyword := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(keyword) > maxSearchKeyword {
		keyword = string([]rune(keyword)[:maxSearchKeyword])
	}

	page, err := u.moderation.List(r.Context(), ModerationQuery{
		Status:  status,
		Keyword: keyword,
		Limit:   webUIPageSize,
		Cursor:  strings.TrimSpace(r.URL.Query().Get("cursor")),
	})
	if err != nil {
		u.logger.ErrorContext(r.Context(), "console list comments", slog.Any("error", err))
		u.render(w, http.StatusInternalServerError, "comments", pageData{
			Title: "评论审核", Nav: "comments", Account: p.Account,
			CSRFToken: u.csrfToken(w, r), Statuses: moderationStatuses, Status: status,
			Keyword: keyword,
			Error:   "读取评论失败，请查看服务日志。",
		})
		return
	}

	// The counts describe the same filter as the rows above them. A failure
	// here is not worth failing the page for: the queue still works without
	// the numbers.
	counts, err := u.moderation.Counts(r.Context(), ModerationQuery{Keyword: keyword})
	if err != nil {
		u.logger.WarnContext(r.Context(), "console count comments", slog.Any("error", err))
		counts = nil
	}

	// The policy is read after the list, so a failure to read it cannot cost the
	// operator the queue: an unknown switch reads as "review required".
	policy, err := u.svc.CommentPolicy(r.Context())
	if err != nil {
		u.logger.WarnContext(r.Context(), "read comment policy", slog.Any("error", err))
	}

	flash, errText := u.notices(r)
	u.render(w, http.StatusOK, "comments", pageData{
		Title: "评论审核", Nav: "comments", Account: p.Account,
		CSRFToken: u.csrfToken(w, r),
		Flash:     flash, Error: errText,
		Statuses: moderationStatuses, Status: status,
		Comments: page.Items, NextCursor: page.NextCursor,
		Keyword: keyword, Counts: counts,
		AutoApprove: policy.AutoApprove,
	})
}

// commentsPolicy flips the auto-approve switch.
//
// It is a form rather than a bare link so a prefetching browser cannot publish
// without a click, and it carries no state of its own: the switch is the only
// field, and its absence means "off".
func (u *webUI) commentsPolicy(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !u.parseForm(w, r) || !u.csrfOK(r) {
		u.redirect(w, r, "/admin/comments?err=csrf")
		return
	}
	policy := CommentPolicy{AutoApprove: r.PostFormValue("autoApprove") != ""}
	if err := u.svc.SetCommentPolicy(r.Context(), policy, u.actor(r, p)); err != nil {
		u.logger.ErrorContext(r.Context(), "save comment policy", slog.Any("error", err))
		u.redirect(w, r, "/admin/comments?err=error")
		return
	}
	u.redirect(w, r, "/admin/comments?msg=policy_saved")
}

// commentStatus applies one moderation decision and records who made it.
func (u *webUI) commentStatus(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !u.parseForm(w, r) || !u.csrfOK(r) {
		u.redirect(w, r, "/admin/comments?err=csrf")
		return
	}
	status := r.PostFormValue("status")
	filter := u.returnFilter(r)
	keyword := u.returnKeyword(r)
	if !validModerationStatus(status) {
		u.redirect(w, r, commentsPath(filter, keyword, "err", "bad_request"))
		return
	}

	comment, err := u.moderation.SetStatus(r.Context(), r.PathValue("id"), status)
	if err != nil {
		u.redirect(w, r, commentsPath(filter, keyword, "err", u.moderationErrorCode(err)))
		return
	}
	u.record(r, p, "comment.moderate", "comment", comment.ID, map[string]any{
		"postSlug": comment.PostSlug,
		"status":   comment.Status,
	})
	u.redirect(w, r, commentsPath(filter, keyword, "msg", "moderated"))
}

// commentDelete removes a comment and its replies.
func (u *webUI) commentDelete(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !u.parseForm(w, r) || !u.csrfOK(r) {
		u.redirect(w, r, "/admin/comments?err=csrf")
		return
	}
	filter := u.returnFilter(r)
	keyword := u.returnKeyword(r)
	id := r.PathValue("id")
	if err := u.moderation.Delete(r.Context(), id); err != nil {
		u.redirect(w, r, commentsPath(filter, keyword, "err", u.moderationErrorCode(err)))
		return
	}
	u.record(r, p, "comment.delete", "comment", id, nil)
	u.redirect(w, r, commentsPath(filter, keyword, "msg", "deleted"))
}

// commentsBulk applies one moderation decision to every selected comment.
//
// One endpoint rather than one per action: the decision is a parameter, so the
// audit detail and the redirect are written once instead of four times.
func (u *webUI) commentsBulk(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !u.parseForm(w, r) || !u.csrfOK(r) {
		u.redirect(w, r, "/admin/comments?err=csrf")
		return
	}
	filter := u.returnFilter(r)
	keyword := u.returnKeyword(r)
	ids := r.PostForm["ids"]
	if len(ids) == 0 {
		u.redirect(w, r, commentsPath(filter, keyword, "err", "no_selection"))
		return
	}

	action := strings.TrimSpace(r.PostFormValue("action"))
	var (
		affected int
		err      error
	)
	switch action {
	case "approve", "reject", "spam":
		status := map[string]string{"approve": "approved", "reject": "rejected", "spam": "spam"}[action]
		affected, err = u.moderation.BulkSetStatus(r.Context(), ids, status)
	case "delete":
		affected, err = u.moderation.BulkDelete(r.Context(), ids)
	default:
		u.redirect(w, r, commentsPath(filter, keyword, "err", "bad_request"))
		return
	}
	if err != nil {
		u.redirect(w, r, commentsPath(filter, keyword, "err", u.moderationErrorCode(err)))
		return
	}

	u.record(r, p, "comment.bulk."+action, "comment", "", map[string]any{
		"requested": len(ids),
		"affected":  affected,
	})
	u.redirect(w, r, commentsPath(filter, keyword, "msg", "bulk_done"))
}

// profileSave updates the display name, the only account field that is editable
// without touching a credential.
func (u *webUI) profileSave(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !u.parseForm(w, r) || !u.csrfOK(r) {
		u.redirect(w, r, "/admin?err=csrf")
		return
	}
	displayName := strings.TrimSpace(r.PostFormValue("displayName"))
	if _, err := u.svc.UpdateAccount(r.Context(), p.Account.ID, UpdateAccountInput{
		DisplayName: &displayName,
	}, u.actor(r, p)); err != nil {
		u.redirect(w, r, "/admin?err="+u.accountErrorCode(err))
		return
	}
	u.redirect(w, r, "/admin?msg=profile_saved")
}

// profilePassword changes the signed-in account's password and signs it out.
func (u *webUI) profilePassword(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !u.parseForm(w, r) || !u.csrfOK(r) {
		u.redirect(w, r, "/admin?err=csrf")
		return
	}
	err := u.svc.ChangePassword(r.Context(), p.Account.ID, ChangePasswordInput{
		CurrentPassword: r.PostFormValue("currentPassword"),
		NewPassword:     r.PostFormValue("password"),
	}, u.actor(r, p))
	if err != nil {
		u.redirect(w, r, "/admin?err="+u.accountErrorCode(err))
		return
	}
	// The change revoked every session of the account, including this one.
	u.clearSessionCookie(w)
	u.redirect(w, r, "/admin/login?msg=password_changed")
}

// mailPage shows the outbound-mail settings. Everything on it is editable: the
// SMTP account, the sender, the recipients and the two notices. A deployment
// that configured mail through the environment has those values imported once
// at startup, and the page owns them afterwards.
func (u *webUI) mailPage(w http.ResponseWriter, r *http.Request, p *Principal) {
	settings, err := u.mail.Settings(r.Context())
	if err != nil {
		u.logger.ErrorContext(r.Context(), "read mail settings", slog.Any("error", err))
		settings = MailSettings{Port: 587, TLS: "auto"}
	}
	flash, errText := u.notices(r)
	u.render(w, http.StatusOK, "mail", pageData{
		Title: "邮箱管理", Nav: "mail", Account: p.Account,
		CSRFToken: u.csrfToken(w, r),
		Flash:     flash, Error: errText,
		Mail: settings,
	})
}

// mailSave stores the whole configuration, including the SMTP credential.
//
// The password field is write-only: the page never receives the stored value,
// so an empty box means "keep it" rather than "erase it", and clearing it has
// to be a deliberate act on the field itself.
func (u *webUI) mailSave(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !u.parseForm(w, r) || !u.csrfOK(r) {
		u.redirect(w, r, "/admin/mail?err=csrf")
		return
	}
	// The stored password is fetched only to keep the page's "已保存" hint
	// truthful when a save is rejected and the form is rendered again.
	stored, _ := u.mail.Settings(r.Context())

	port, portErr := strconv.Atoi(strings.TrimSpace(r.PostFormValue("port")))
	in := MailSettings{
		Host:        strings.TrimSpace(r.PostFormValue("host")),
		Port:        port,
		TLS:         strings.TrimSpace(r.PostFormValue("tls")),
		Username:    strings.TrimSpace(r.PostFormValue("username")),
		Password:    r.PostFormValue("password"),
		HasPassword: stored.HasPassword && r.PostFormValue("password") == "",
		From:        strings.TrimSpace(r.PostFormValue("from")),
		FromName:    strings.TrimSpace(r.PostFormValue("fromName")),
		AdminTo:     splitRecipients(r.PostFormValue("adminTo")),
		ReplyNotify: r.PostFormValue("replyNotify") != "",
		AdminNotify: r.PostFormValue("adminNotify") != "",
		// Whole documents: trim the pasted edges, keep every byte inside.
		TemplateAdmin: strings.TrimSpace(r.PostFormValue("templateAdmin")),
		TemplateReply: strings.TrimSpace(r.PostFormValue("templateReply")),
		OwnerKey:      strings.TrimSpace(r.PostFormValue("ownerKey")),
	}
	if portErr != nil {
		// A rejected save re-renders the form with what was typed. Throwing the
		// operator back to an empty page over a typo is how "it won't save"
		// happens.
		u.mailFormError(w, r, p, in, "端口要填数字，通常是 465（SSL）或 587（STARTTLS）。")
		return
	}
	// A short key is worse than none: it invites the owner to believe the badge
	// is protected when the value could be guessed.
	if in.OwnerKey != "" && len([]rune(in.OwnerKey)) < minOwnerKeyLength {
		u.mailFormError(w, r, p, in, "站长令牌至少 16 位，短了不如不设。")
		return
	}
	if err := u.mail.SaveSettings(r.Context(), in); err != nil {
		u.logger.WarnContext(r.Context(), "save mail settings", slog.Any("error", err))
		u.mailFormError(w, r, p, in, mailValidationText(err))
		return
	}
	// The credential is never written to the trail; what an auditor needs is
	// that it changed, not what it became.
	u.record(r, p, actionSettingsUpdate, "settings", "mail", map[string]any{
		"host":            in.Host,
		"port":            in.Port,
		"tls":             in.TLS,
		"from":            in.From,
		"adminTo":         in.AdminTo,
		"replyNotify":     in.ReplyNotify,
		"adminNotify":     in.AdminNotify,
		"passwordChanged": in.Password != "",
	})
	u.redirect(w, r, "/admin/mail?msg=mail_saved")
}

// mailFormError renders the settings form again with the submitted values and
// the reason they were refused, so a typo never costs the operator the rest of
// the form.
func (u *webUI) mailFormError(w http.ResponseWriter, r *http.Request, p *Principal, in MailSettings, message string) {
	u.render(w, http.StatusBadRequest, "mail", pageData{
		Title: "邮箱管理", Nav: "mail", Account: p.Account,
		CSRFToken: u.csrfToken(w, r),
		Error:     message,
		Mail:      in,
	})
}

// mailValidationText names the field that was refused. The mail package speaks
// field codes, not Chinese, so the wording lives here.
func mailValidationText(err error) string {
	labels := map[string]string{
		"host":     "服务器地址",
		"port":     "端口",
		"tls":      "加密方式",
		"from":     "发件邮箱",
		"adminTo":  "管理员收件邮箱",
		"username": "用户名",
	}
	for _, field := range apperr.Fields(err) {
		if label, ok := labels[field.Field]; ok {
			return "「" + label + "」填得不对，检查一下格式再保存。"
		}
	}
	return "邮件设置没有保存，检查服务器地址、端口和邮箱格式。"
}

// mailTest sends one message before answering, so a wrong password is a message
// on this page rather than a warning in a log nobody is reading.
func (u *webUI) mailTest(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !u.parseForm(w, r) || !u.csrfOK(r) {
		u.redirect(w, r, "/admin/mail?err=csrf")
		return
	}
	to := strings.TrimSpace(r.PostFormValue("to"))
	if to == "" {
		u.redirect(w, r, "/admin/mail?err=bad_request")
		return
	}
	if err := u.mail.SendTest(r.Context(), to); err != nil {
		u.logger.WarnContext(r.Context(), "send test mail", slog.Any("error", err))
		settings, _ := u.mail.Settings(r.Context())
		// The mail server's own words, verbatim: "535 Login Fail" is the only
		// place that answer exists, and paraphrasing it as "发送失败" would send
		// the operator hunting in a log file.
		u.render(w, http.StatusBadGateway, "mail", pageData{
			Title: "邮箱管理", Nav: "mail", Account: p.Account,
			CSRFToken: u.csrfToken(w, r),
			Error:     "测试邮件没有发出去：" + shortMailError(err),
			Mail:      settings,
		})
		return
	}
	u.redirect(w, r, "/admin/mail?msg=mail_test_sent")
}

// shortMailError trims the wrapping this console added and keeps the server's
// reply, bounded so a chatty MTA cannot fill the page.
func shortMailError(err error) string {
	text := strings.TrimPrefix(err.Error(), "send test mail: ")
	if runes := []rune(text); len(runes) > 300 {
		return string(runes[:300]) + "…"
	}
	return text
}

// splitRecipients accepts however the operator separated the addresses.
func splitRecipients(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, duplicate := seen[strings.ToLower(field)]; duplicate {
			continue
		}
		seen[strings.ToLower(field)] = struct{}{}
		out = append(out, field)
	}
	return out
}

// auditPage reads the trail, newest first.
func (u *webUI) auditPage(w http.ResponseWriter, r *http.Request, p *Principal) {
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	position, err := cursor.Decode(r.URL.Query().Get("cursor"))
	if err != nil {
		u.render(w, http.StatusBadRequest, "audit", pageData{
			Title: "最近活动", Nav: "audit", Account: p.Account,
			CSRFToken: u.csrfToken(w, r), Error: "分页令牌无效，请从第一页重新开始。",
		})
		return
	}

	q := audit.Query{Limit: 100, Action: action}
	if position.ID != "" {
		q.Cursor = &position
	}
	entries, next, err := u.svc.ListAuditLogs(r.Context(), q)
	if err != nil {
		u.logger.ErrorContext(r.Context(), "console list audit", slog.Any("error", err))
		u.render(w, http.StatusInternalServerError, "audit", pageData{
			Title: "最近活动", Nav: "audit", Account: p.Account,
			CSRFToken: u.csrfToken(w, r), Error: "读取最近活动失败，请查看服务日志。",
		})
		return
	}

	flash, errText := u.notices(r)
	u.render(w, http.StatusOK, "audit", pageData{
		Title: "最近活动", Nav: "audit", Account: p.Account,
		CSRFToken: u.csrfToken(w, r),
		Flash:     flash, Error: errText,
		Entries: entries, Action: action, NextCursor: next,
	})
}

// auditBulk removes selected entries from the trail.
//
// This is the only write the activity page offers. It is deliberately narrow:
// the trail is how an operator answers "who deleted that comment", so every
// removal is an explicit selection and is itself recorded under audit.delete.
func (u *webUI) auditBulk(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !u.parseForm(w, r) || !u.csrfOK(r) {
		u.redirect(w, r, "/admin/audit?err=csrf")
		return
	}
	filter := strings.TrimSpace(r.PostFormValue("return"))
	if len(r.PostForm["ids"]) == 0 {
		u.redirect(w, r, auditPath(filter, "err", "no_selection"))
		return
	}

	if _, err := u.svc.DeleteAuditLogs(r.Context(), r.PostForm["ids"], u.actor(r, p)); err != nil {
		code := "error"
		if fields := apperr.Fields(err); len(fields) > 0 {
			code = "bad_request"
		}
		u.logger.ErrorContext(r.Context(), "console delete audit entries", slog.Any("error", err))
		u.redirect(w, r, auditPath(filter, "err", code))
		return
	}
	u.redirect(w, r, auditPath(filter, "msg", "activity_deleted"))
}

// render executes the page and writes it with the console's security headers.
//
// The template is rendered into a buffer first: a template error after the
// status line is sent would leave a half-written page with a 200 on it, and the
// only honest answer to "the page failed to render" is a 500.
func (u *webUI) render(w http.ResponseWriter, status int, page string, data pageData) {
	data.HasModeration = u.moderation != nil
	data.HasMail = u.mail != nil
	data.AssetVersion = consoleAssetVersion

	tmpl, ok := uiPages[page]
	if !ok {
		u.logger.Error("console template is not registered", slog.String("page", page))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		u.logger.Error("render console page", slog.String("page", page), slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	// The console loads nothing from anywhere: no fonts, no remote images, no
	// scripts. data: images are the one exception — the checkbox tick is a tiny
	// inline SVG in app.css, and without img-src it would be blocked and render
	// as an empty green square.
	header.Set("Content-Security-Policy",
		"default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// asset serves one embedded file with a content-addressed cache policy.
func (u *webUI) asset(body []byte, contentType string) http.HandlerFunc {
	etag := `"` + assetHash(body) + `"`
	return func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Type", contentType)
		header.Set("Cache-Control", "public, max-age=31536000, immutable")
		header.Set("ETag", etag)
		header.Set("X-Content-Type-Options", "nosniff")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(body)
	}
}

// snoozed reports whether the operator dismissed the initial-password reminder
// within its ten-minute window.
func (u *webUI) snoozed(r *http.Request) bool {
	cookie, err := r.Cookie(snoozeCookieName)
	return err == nil && cookie.Value == "1"
}

// mustAsset reads an embedded file or fails at startup: a missing stylesheet is
// a build mistake, not a page that should render unstyled.
func mustAsset(name string) []byte {
	body, err := webUIAssets.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("admin: read embedded asset %s: %v", name, err))
	}
	return body
}

// assetHash is the cache key: a short digest of the exact bytes served.
func assetHash(parts ...[]byte) string {
	sum := sha256.New()
	for _, part := range parts {
		_, _ = sum.Write(part)
	}
	return hex.EncodeToString(sum.Sum(nil))[:12]
}

// redirect answers with 303 so a form post lands on a GET.
func (u *webUI) redirect(w http.ResponseWriter, r *http.Request, location string) {
	http.Redirect(w, r, location, http.StatusSeeOther)
}

// setSessionCookie stores the bearer token in an HttpOnly, SameSite=Strict
// cookie scoped to /admin.
//
// HttpOnly keeps it away from any script; SameSite=Strict means a form on
// another site cannot even send the cookie, which is the first line of defence.
// The CSRF token below is the second: SameSite rules have gaps on older
// browsers, and a console must not rely on one mechanism.
func (u *webUI) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   u.secureCookies,
		SameSite: http.SameSiteStrictMode,
		Expires:  expires,
		MaxAge:   maxAge,
	})
}

// clearSessionCookie expires the session cookie.
func (u *webUI) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   u.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// csrfToken returns the token for the current browser, minting one when this is
// the first form it has seen.
//
// The cookie is HttpOnly and the token is rendered server-side into the form,
// so it is a synchroniser token without a session column: the pair is compared
// on every state-changing post. It is not a secret the page needs to read.
func (u *webUI) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(csrfCookieName); err == nil && len(cookie.Value) >= 32 {
		return cookie.Value
	}
	token := randomToken()
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   u.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(csrfCookieTTL.Seconds()),
	})
	return token
}

// csrfOK compares the submitted field with the cookie in constant time.
func (u *webUI) csrfOK(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	submitted := r.PostFormValue(csrfFormField)
	if submitted == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(submitted), []byte(cookie.Value)) == 1
}

// parseForm reads the body under a size cap. A failure means the body was not a
// form at all, which every caller answers with a redirect and an error code.
func (u *webUI) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, webUIMaxBodyBytes)
	return r.ParseForm() == nil
}

// randomToken mints a 256-bit URL-safe token.
func randomToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// Without entropy there is no safe CSRF token; failing the render is
		// better than handing out a predictable one.
		panic(fmt.Sprintf("admin: read random bytes: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// actor builds the audit identity of a console request.
func (u *webUI) actor(r *http.Request, p *Principal) audit.Actor {
	ip, userAgent := requestOrigin(r)
	return audit.Actor{ID: p.Account.ID, Name: p.Account.Username, IP: ip, UserAgent: userAgent}
}

// record appends to the trail, logging rather than failing when it cannot.
func (u *webUI) record(r *http.Request, p *Principal, action, resource, resourceID string, detail map[string]any) {
	if u.auditor == nil {
		return
	}
	event := audit.Event{
		Actor:      u.actor(r, p),
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		Detail:     detail,
	}
	if err := u.auditor.Record(r.Context(), event); err != nil {
		u.logger.WarnContext(r.Context(), "record console audit event",
			slog.String("action", action), slog.Any("error", err))
	}
}

// returnFilter reads the moderation state the operator was looking at, so a
// decision sends them back to the same tab instead of the default one.
func (u *webUI) returnFilter(r *http.Request) string {
	if status := strings.TrimSpace(r.PostFormValue("return")); validModerationStatus(status) {
		return status
	}
	return "pending"
}

// returnKeyword reads the search the operator was running, so a decision sends
// them back to the same result set instead of the unfiltered queue.
func (u *webUI) returnKeyword(r *http.Request) string {
	keyword := strings.TrimSpace(r.PostFormValue("q"))
	if runes := []rune(keyword); len(runes) > maxSearchKeyword {
		return string(runes[:maxSearchKeyword])
	}
	return keyword
}

// commentsPath builds a queue URL that preserves the tab and the search.
//
// Values are escaped by url.Values rather than concatenated: a keyword with an
// ampersand in it would otherwise split into two parameters and drop the rest
// of the search on the way back.
func commentsPath(status, keyword, key, value string) string {
	query := url.Values{}
	query.Set("status", status)
	if keyword != "" {
		query.Set("q", keyword)
	}
	if key != "" && value != "" {
		query.Set(key, value)
	}
	return "/admin/comments?" + query.Encode()
}

// auditPath builds an activity URL that keeps the action filter.
//
// Same reasoning as commentsPath: url.Values escapes the filter, so an action
// name or a code that happens to contain punctuation cannot split the query.
func auditPath(filter, key, value string) string {
	query := url.Values{}
	if filter != "" {
		query.Set("action", filter)
	}
	if key != "" && value != "" {
		query.Set(key, value)
	}
	if len(query) == 0 {
		return "/admin/audit"
	}
	return "/admin/audit?" + query.Encode()
}

// moderationErrorCode maps a queue failure to a query code the page can explain.
func (u *webUI) moderationErrorCode(err error) string {
	switch {
	case errors.Is(err, apperr.ErrNotFound):
		return "not_found"
	case errors.Is(err, apperr.ErrInvalid):
		return "bad_request"
	default:
		u.logger.Error("console moderation action", slog.Any("error", err))
		return "error"
	}
}

// accountErrorCode maps an account failure to a query code.
func (u *webUI) accountErrorCode(err error) string {
	if errors.Is(err, apperr.ErrConflict) {
		return "conflict_account"
	}
	if fields := apperr.Fields(err); len(fields) > 0 {
		for _, field := range fields {
			if field.Field == "currentPassword" {
				return "bad_current"
			}
		}
		return "invalid_account"
	}
	u.logger.Error("console account action", slog.Any("error", err))
	return "error"
}

// notices turns the post-redirect codes into sentences. Unknown codes render
// nothing: the message text lives here, not in the URL, so a link cannot put
// arbitrary copy on the page.
func (u *webUI) notices(r *http.Request) (flash, errText string) {
	query := r.URL.Query()
	if text, ok := flashMessages[query.Get("msg")]; ok {
		flash = text
	}
	if text, ok := errorMessages[query.Get("err")]; ok {
		errText = text
	}
	return flash, errText
}

var flashMessages = map[string]string{
	"moderated":        "已更新评论状态。",
	"deleted":          "已删除。",
	"created":          "已创建。",
	"updated":          "已更新。",
	"password_changed": "密码已更新；该账号的会话已全部撤销。",
	"logged_out":       "已退出登录。",
	"bulk_done":        "已对选中的条目执行批量操作。",
	"profile_saved":    "已保存账号资料。",
	"mail_saved":       "已保存邮件设置。",
	"mail_test_sent":   "SMTP 已接收这封测试邮件，去收件箱（含垃圾箱）确认一下。",
	"bulk_partial":     "批量操作已执行，但有部分条目因保护规则被跳过。",
	"activity_deleted": "已删除选中的活动记录。",
	"policy_saved":     "已保存审核策略。",
}

var errorMessages = map[string]string{
	"csrf":             "表单已过期，请刷新页面后重试。",
	"bad_request":      "请求不合法，请重试。",
	"not_found":        "对象不存在，可能已被删除。",
	"conflict_account": "操作被拒绝：不能停用或删除当前登录的账号，也不能停用最后一个活跃管理员或删除最后一个账号。",
	"invalid_account":  "输入不合法：用户名 3–40 位小写字母、数字、. _ -；密码至少 12 位，且不能与用户名相同。",
	"invalid_password": "密码不合法：至少 12 位，不能与用户名或当前密码相同。",
	"bad_current":      "当前密码不正确。",
	"no_selection":     "没有选中任何条目。",
	"error":            "操作失败，请查看服务日志。",
}

// validModerationStatus reports whether s is a state the console can list or
// set. It is checked before the value reaches the adapter.
func validModerationStatus(s string) bool {
	for _, allowed := range moderationStatuses {
		if s == allowed {
			return true
		}
	}
	return false
}

// greeting returns the part of the day the operator is in, by the server's
// clock. It is a courtesy line, not a fact about the reader: a dashboard that
// guessed the visitor's timezone would need the reader's clock to be right.
func greeting(now time.Time) string {
	switch hour := now.Hour(); {
	case hour < 5:
		return "夜深了"
	case hour < 11:
		return "早上好"
	case hour < 14:
		return "中午好"
	case hour < 18:
		return "下午好"
	default:
		return "晚上好"
	}
}

// dateLine is the dashboard's calendar line: "2026年11月15日 周日".
func dateLine(now time.Time) string {
	weekdays := [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	return fmt.Sprintf("%d年%d月%d日 %s", now.Year(), int(now.Month()), now.Day(), weekdays[int(now.Weekday())])
}

// actionLabel turns a dotted audit action into something a person reads.
func actionLabel(action string) string {
	if label, ok := actionLabels[action]; ok {
		return label
	}
	return action
}

var actionLabels = map[string]string{
	"auth.login":              "登录",
	"auth.logout":             "退出登录",
	"account.create":          "创建账号",
	"account.update":          "更新账号",
	"account.delete":          "删除账号",
	"account.password_change": "修改密码",
	"account.bootstrap":       "引导首个账号",
	"session.revoke":          "撤销会话",
	"comment.moderate":        "审核评论",
	"comment.delete":          "删除评论",
	"comment.bulk.approve":    "批量通过评论",
	"comment.bulk.reject":     "批量拒绝评论",
	"comment.bulk.spam":       "批量标记垃圾",
	"comment.bulk.delete":     "批量删除评论",
	"audit.delete":            "删除活动记录",
}

// relativeTime renders a timestamp the way an operator reads it: "3 分钟前" for
// something recent, an absolute date once the relative form stops being useful.
// The exact value stays available in the element's title attribute.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "从未"
	}
	elapsed := time.Since(t)
	switch {
	case elapsed < time.Minute:
		return "刚刚"
	case elapsed < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(elapsed.Hours()))
	case elapsed < 7*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(elapsed.Hours()/24))
	default:
		return t.Local().Format("2006-01-02")
	}
}

// firstRune returns the first character of s, uppercased, for an avatar. Runes
// rather than bytes so a Chinese display name does not render as a broken
// byte pair.
func firstRune(s string) string {
	for _, r := range strings.TrimSpace(s) {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// statusBadge maps a state to one of the console's badge modifiers.
//
// The modifier carries the colour, so a state can never be rendered with the
// wrong one: there is no second class to forget next to it.
func statusBadge(status any) string {
	switch fmt.Sprint(status) {
	case "pending":
		return "badge--pending"
	case "approved", "active":
		return "badge--approved"
	case "rejected", "disabled":
		return "badge--rejected"
	case "spam":
		return "badge--spam"
	default:
		return "badge--rejected"
	}
}

// statusLabel is the Chinese label for a comment or account state.
//
// It takes any because template data carries both plain strings (comment
// statuses) and typed strings (AccountStatus); the template engine refuses to
// pass one where the other is declared.
func statusLabel(status any) string {
	value := fmt.Sprint(status)
	if label, ok := statusLabels[value]; ok {
		return label
	}
	return value
}

var statusLabels = map[string]string{
	"pending":  "待审",
	"approved": "已通过",
	"rejected": "已拒绝",
	"spam":     "垃圾",
	"active":   "启用",
	"disabled": "已停用",
}

// uiPages holds one parsed template per page, each with the shared layout.
var uiPages = parseUIPages()

func parseUIPages() map[string]*template.Template {
	pages := map[string]*template.Template{}
	for _, name := range []string{"login", "dashboard", "comments", "audit", "mail"} {
		pages[name] = template.Must(template.New("layout").Funcs(template.FuncMap{
			"fmtTime":     func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") },
			"relTime":     relativeTime,
			"firstRune":   firstRune,
			"statusLabel": statusLabel,
			"statusBadge": statusBadge,
			"actionLabel": actionLabel,
			// eqStr compares a typed string (AccountStatus) with a template
			// literal; the built-in eq refuses the mismatch.
			"eqStr": func(value any, want string) bool { return fmt.Sprint(value) == want },
			"jsonDetail": func(detail map[string]any) string {
				if len(detail) == 0 {
					return ""
				}
				encoded, err := json.Marshal(detail)
				if err != nil {
					return ""
				}
				return string(encoded)
			},
		}).ParseFS(webUITemplates, "webui_templates/layout.html", "webui_templates/"+name+".html"))
	}
	return pages
}
