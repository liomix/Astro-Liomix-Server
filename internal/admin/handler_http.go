package admin

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

// DefaultMaxBodyBytes caps request bodies on this module's endpoints. The
// largest legitimate payload is a create-account request: three short strings.
const DefaultMaxBodyBytes int64 = 16 << 10

// Limits on the values copied into the session and audit rows.
const (
	maxUserAgentLength = 512
	maxIPLength        = 64
)

// HTTPConfig carries the transport-level tuning of the admin routes. The zero
// value is usable: it applies DefaultMaxBodyBytes.
type HTTPConfig struct {
	// MaxBodyBytes caps the size of an incoming request body.
	MaxBodyBytes int64
}

// RegisterRoutes mounts the administrator endpoints on mux.
//
// Everything except the login route is wrapped in authenticating middleware, so
// a handler never has to remember to check the token. The login route is public
// by definition — it is where a token comes from.
func RegisterRoutes(mux *http.ServeMux, logger *slog.Logger, svc *Service, cfg HTTPConfig) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}

	h := &adminHandler{svc: svc, logger: logger, maxBodySize: cfg.MaxBodyBytes}

	mux.HandleFunc("POST /api/v1/admin/auth/login", h.login)
	mux.Handle("POST /api/v1/admin/auth/logout", h.authenticated(http.HandlerFunc(h.logout)))
	mux.Handle("GET /api/v1/admin/auth/session", h.authenticated(http.HandlerFunc(h.session)))

	mux.Handle("GET /api/v1/admin/accounts", h.authenticated(http.HandlerFunc(h.listAccounts)))
	mux.Handle("POST /api/v1/admin/accounts", h.authenticated(http.HandlerFunc(h.createAccount)))
	mux.Handle("GET /api/v1/admin/accounts/{id}", h.authenticated(http.HandlerFunc(h.getAccount)))
	mux.Handle("PATCH /api/v1/admin/accounts/{id}", h.authenticated(http.HandlerFunc(h.updateAccount)))
	mux.Handle("DELETE /api/v1/admin/accounts/{id}", h.authenticated(http.HandlerFunc(h.deleteAccount)))
	mux.Handle("POST /api/v1/admin/accounts/{id}/password", h.authenticated(http.HandlerFunc(h.changePassword)))
	mux.Handle("GET /api/v1/admin/accounts/{id}/sessions", h.authenticated(http.HandlerFunc(h.listSessions)))
	mux.Handle("DELETE /api/v1/admin/accounts/{id}/sessions/{sessionId}", h.authenticated(http.HandlerFunc(h.revokeSession)))

	if svc.HasAuditTrail() {
		mux.Handle("GET /api/v1/admin/audit-logs", h.authenticated(http.HandlerFunc(h.listAuditLogs)))
	}
}

// adminHandler serves the administrator endpoints.
type adminHandler struct {
	svc         *Service
	logger      *slog.Logger
	maxBodySize int64
}

// principalKey is the context key holding the authenticated request's identity.
// It is an unexported struct type, so no other package can collide with it.
type principalKey struct{}

// Principal is who is making the request: the account and the session its token
// belongs to.
type Principal struct {
	Account *Account
	Session *Session
}

// principalFrom returns the identity the middleware stored, if any.
func principalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok && p != nil
}

// authenticated rejects a request that does not carry a live session.
func (h *adminHandler) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account, session, err := h.svc.Authenticate(r.Context(), httpkit.BearerToken(r))
		if err != nil {
			h.writeAuthError(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, &Principal{Account: account, Session: session})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requirePrincipal returns the authenticated identity and writes the 401 itself
// when it is missing. The middleware guarantees it is there; the check exists so
// a future route mounted without the wrapper fails closed instead of panicking.
func (h *adminHandler) requirePrincipal(w http.ResponseWriter, r *http.Request) (*Principal, bool) {
	p, ok := principalFrom(r.Context())
	if !ok {
		h.writeAuthError(w, r, apperr.ErrUnauthorized)
		return nil, false
	}
	return p, true
}

// writeAuthError answers 401 with the challenge RFC 9110 requires, or maps any
// other failure the way every other endpoint does.
func (h *adminHandler) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, apperr.ErrUnauthorized) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
		httpkit.WriteError(w, r, h.logger, apperr.ErrUnauthorized)
		return
	}
	httpkit.WriteError(w, r, h.logger, err)
}

// AuthorizeRequest resolves a request's administrator credential on behalf of
// another module — the comment module accepts an admin session as a moderation
// credential, so it needs the same check without the 401 response.
//
// A false return means "not an administrator", not "authentication failed":
// the caller decides whether the request has another credential to try and
// writes its own challenge. Errors are swallowed for the same reason — the
// other module's 401 is the right answer either way, and logging here would
// duplicate its log line.
func (s *Service) AuthorizeRequest(r *http.Request) (*audit.Actor, bool) {
	token := httpkit.BearerToken(r)
	if token == "" {
		return nil, false
	}
	account, _, err := s.Authenticate(r.Context(), token)
	if err != nil {
		return nil, false
	}
	ip, userAgent := requestOrigin(r)
	return &audit.Actor{ID: account.ID, Name: account.Username, IP: ip, UserAgent: userAgent}, true
}

// login exchanges a username and password for a session token.
//
// The response is 200, not 201: a session is a credential, and there is no
// useful Location to hand back — the client already knows where it is.
func (h *adminHandler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpkit.DecodeJSON(w, r, h.maxBodySize, &req); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	ip, userAgent := requestOrigin(r)
	account, session, token, err := h.svc.Login(r.Context(), LoginInput{
		Username:  req.Username,
		Password:  req.Password,
		IP:        ip,
		UserAgent: userAgent,
	})
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, loginResponse{
		Token:     token,
		ExpiresAt: session.ExpiresAt,
		Account:   toAccountPayload(account),
	})
}

// logout revokes the session that made the request.
func (h *adminHandler) logout(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requirePrincipal(w, r)
	if !ok {
		return
	}
	if err := h.svc.Logout(r.Context(), p.Session.ID, h.actor(r, p)); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusNoContent, nil)
}

// session answers who the caller is and when the session ends.
func (h *adminHandler) session(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requirePrincipal(w, r)
	if !ok {
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, currentSessionResponse{
		Account: toAccountPayload(p.Account),
		Session: toSessionPayload(p.Session, p.Session.ID),
	})
}

// listAccounts returns one page of accounts, newest first.
func (h *adminHandler) listAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePrincipal(w, r); !ok {
		return
	}
	query := r.URL.Query()

	limit, err := queryInt(r, "limit")
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	position, err := cursor.Decode(query.Get("cursor"))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	q := AccountQuery{Limit: limit, Status: AccountStatus(strings.TrimSpace(query.Get("status")))}
	if position.ID != "" {
		q.Cursor = &position
	}

	accounts, nextCursor, err := h.svc.ListAccounts(r.Context(), q)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	page := listAccountsResponse{Items: toAccountPayloads(accounts)}
	if nextCursor != "" {
		page.NextCursor = &nextCursor
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, page)
}

// createAccount adds an operator.
func (h *adminHandler) createAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req createAccountRequest
	if err := httpkit.DecodeJSON(w, r, h.maxBodySize, &req); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	account, err := h.svc.CreateAccount(r.Context(), CreateAccountInput{
		Username:    req.Username,
		DisplayName: req.DisplayName,
		Password:    req.Password,
	}, h.actor(r, p))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	w.Header().Set("Location", "/api/v1/admin/accounts/"+account.ID)
	httpkit.WriteJSON(w, h.logger, http.StatusCreated, toAccountPayload(account))
}

// getAccount returns one account.
func (h *adminHandler) getAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePrincipal(w, r); !ok {
		return
	}
	account, err := h.svc.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, toAccountPayload(account))
}

// updateAccount changes a display name or a status.
func (h *adminHandler) updateAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req updateAccountRequest
	if err := httpkit.DecodeJSON(w, r, h.maxBodySize, &req); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	in := UpdateAccountInput{DisplayName: req.DisplayName}
	if req.Status != nil {
		status := AccountStatus(strings.TrimSpace(*req.Status))
		in.Status = &status
	}
	account, err := h.svc.UpdateAccount(r.Context(), r.PathValue("id"), in, h.actor(r, p))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, toAccountPayload(account))
}

// deleteAccount removes an operator and every session it opened.
func (h *adminHandler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requirePrincipal(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteAccount(r.Context(), r.PathValue("id"), h.actor(r, p)); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusNoContent, nil)
}

// changePassword sets a new password, for oneself or for another account.
func (h *adminHandler) changePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requirePrincipal(w, r)
	if !ok {
		return
	}
	var req changePasswordRequest
	if err := httpkit.DecodeJSON(w, r, h.maxBodySize, &req); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	err := h.svc.ChangePassword(r.Context(), r.PathValue("id"), ChangePasswordInput{
		CurrentPassword: req.CurrentPassword,
		NewPassword:     req.NewPassword,
	}, h.actor(r, p))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	// The response is 204 even when the caller changed their own password and
	// just revoked their session: the write happened, and the next request
	// will say 401. Answering 401 here would be reporting the consequence
	// before the client has a chance to show "sign in again".
	httpkit.WriteJSON(w, h.logger, http.StatusNoContent, nil)
}

// listSessions returns the live sessions of one account.
func (h *adminHandler) listSessions(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requirePrincipal(w, r)
	if !ok {
		return
	}
	sessions, err := h.svc.ListSessions(r.Context(), r.PathValue("id"))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	items := make([]sessionPayload, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, toSessionPayload(s, p.Session.ID))
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, listSessionsResponse{Items: items})
}

// revokeSession signs one session out.
func (h *adminHandler) revokeSession(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requirePrincipal(w, r)
	if !ok {
		return
	}
	err := h.svc.RevokeSession(r.Context(), r.PathValue("id"), r.PathValue("sessionId"), h.actor(r, p))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusNoContent, nil)
}

// listAuditLogs returns one page of the operator trail.
func (h *adminHandler) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePrincipal(w, r); !ok {
		return
	}
	query := r.URL.Query()

	limit, err := queryInt(r, "limit")
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	position, err := cursor.Decode(query.Get("cursor"))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	q := audit.Query{
		Limit:   limit,
		Action:  strings.TrimSpace(query.Get("action")),
		ActorID: strings.TrimSpace(query.Get("actorId")),
	}
	if position.ID != "" {
		q.Cursor = &position
	}

	entries, nextCursor, err := h.svc.ListAuditLogs(r.Context(), q)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	items := make([]auditLogPayload, 0, len(entries))
	for _, e := range entries {
		items = append(items, auditLogPayload{
			ID:         e.ID,
			ActorID:    e.Actor.ID,
			ActorName:  e.Actor.Name,
			Action:     e.Action,
			Resource:   e.Resource,
			ResourceID: e.ResourceID,
			Detail:     e.Detail,
			IP:         e.Actor.IP,
			UserAgent:  e.Actor.UserAgent,
			CreatedAt:  e.At,
		})
	}
	page := listAuditLogsResponse{Items: items}
	if nextCursor != "" {
		page.NextCursor = &nextCursor
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, page)
}

// actor builds the audit identity for an authenticated request.
func (h *adminHandler) actor(r *http.Request, p *Principal) audit.Actor {
	ip, userAgent := requestOrigin(r)
	return audit.Actor{ID: p.Account.ID, Name: p.Account.Username, IP: ip, UserAgent: userAgent}
}

// queryInt reads an optional integer query parameter.
func queryInt(r *http.Request, name string) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &apperr.ValidationError{Field: name, Code: apperr.FieldInvalidFormat, Message: "must be an integer"}
	}
	if value < 0 {
		return 0, &apperr.ValidationError{Field: name, Code: apperr.FieldOutOfRange, Message: "must not be negative"}
	}
	return value, nil
}

// requestOrigin extracts the client address and user agent for the session and
// audit rows.
//
// RemoteAddr is used as-is rather than X-Forwarded-For: that header is
// client-supplied unless a trusted proxy rewrites it, and a field an attacker
// controls is worse than a field that names the proxy. The audit trail still
// answers "which connection", which is what an incident review needs.
func requestOrigin(r *http.Request) (ip, userAgent string) {
	host := r.RemoteAddr
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	return truncateRunes(host, maxIPLength), truncateRunes(r.UserAgent(), maxUserAgentLength)
}

// truncateRunes cuts a string to at most max characters, counting runes so a
// multi-byte user agent cannot be cut mid-character and rejected by the text
// column.
func truncateRunes(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	return string([]rune(value)[:max])
}
