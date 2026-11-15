package admin

import "time"

// loginRequest is the accepted body for POST /admin/auth/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// createAccountRequest is the accepted body for POST /admin/accounts.
type createAccountRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

// updateAccountRequest is the accepted body for PATCH /admin/accounts/{id}.
//
// Both fields are pointers so the handler can tell "leave it alone" from
// "set it to this value". Without that, a PATCH carrying only displayName
// would silently reset the status to the zero value.
type updateAccountRequest struct {
	DisplayName *string `json:"displayName"`
	Status      *string `json:"status"`
}

// changePasswordRequest is the accepted body for
// POST /admin/accounts/{id}/password.
type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// accountPayload is the wire representation of an account.
//
// PasswordHash is absent, deliberately and permanently: a digest is still a
// credential, and no client has a reason to see one. LastLoginAt is a pointer
// so "never signed in" is an explicit null rather than a zero timestamp.
type accountPayload struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"displayName"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
}

// toAccountPayload maps the entity to its wire form, dropping private fields.
func toAccountPayload(a *Account) accountPayload {
	payload := accountPayload{
		ID:          a.ID,
		Username:    a.Username,
		DisplayName: a.DisplayName,
		Status:      string(a.Status),
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
	}
	if !a.LastLoginAt.IsZero() {
		// A copy per call: taking the address of the entity's field would
		// alias the caller's value.
		lastLogin := a.LastLoginAt
		payload.LastLoginAt = &lastLogin
	}
	return payload
}

// toAccountPayloads maps a page, preserving order.
func toAccountPayloads(accounts []*Account) []accountPayload {
	items := make([]accountPayload, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, toAccountPayload(a))
	}
	return items
}

// loginResponse is what a successful sign-in returns. The token is the only
// copy the client will ever get: the server stores a digest.
type loginResponse struct {
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expiresAt"`
	Account   accountPayload `json:"account"`
}

// sessionPayload is the wire representation of a session. TokenHash is absent:
// it is not a credential the client can use, and publishing it would only make
// a database dump easier to correlate with a response body.
type sessionPayload struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
	// Current marks the session that authenticated this request, so a console
	// can label it instead of offering a button that signs the user out of the
	// page they are looking at without saying so.
	Current bool `json:"current"`
}

// toSessionPayload maps a session to its wire form.
func toSessionPayload(s *Session, currentSessionID string) sessionPayload {
	return sessionPayload{
		ID:         s.ID,
		CreatedAt:  s.CreatedAt,
		LastSeenAt: s.LastSeenAt,
		ExpiresAt:  s.ExpiresAt,
		IP:         s.IP,
		UserAgent:  s.UserAgent,
		Current:    s.ID == currentSessionID,
	}
}

// currentSessionResponse answers GET /admin/auth/session: the account the token
// belongs to and the session itself.
type currentSessionResponse struct {
	Account accountPayload `json:"account"`
	Session sessionPayload `json:"session"`
}

// listAccountsResponse is one page of accounts. NextCursor is null on the last
// page.
type listAccountsResponse struct {
	Items      []accountPayload `json:"items"`
	NextCursor *string          `json:"nextCursor"`
}

// listSessionsResponse lists the live sessions of one account.
//
// It has no cursor: a person has a handful of sessions, and the service caps
// the list rather than paging a set that never grows large.
type listSessionsResponse struct {
	Items []sessionPayload `json:"items"`
}

// auditLogPayload is the wire representation of one trail entry.
type auditLogPayload struct {
	ID         string         `json:"id"`
	ActorID    string         `json:"actorId"`
	ActorName  string         `json:"actorName"`
	Action     string         `json:"action"`
	Resource   string         `json:"resource"`
	ResourceID string         `json:"resourceId"`
	Detail     map[string]any `json:"detail"`
	IP         string         `json:"ip"`
	UserAgent  string         `json:"userAgent"`
	CreatedAt  time.Time      `json:"createdAt"`
}

// listAuditLogsResponse is one page of the trail.
type listAuditLogsResponse struct {
	Items      []auditLogPayload `json:"items"`
	NextCursor *string           `json:"nextCursor"`
}
