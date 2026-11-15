package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/config"
	"github.com/liomix/astro-liomix-server/internal/platform/mail"
	"github.com/liomix/astro-liomix-server/internal/platform/spam"
)

// notificationDrainTimeout is how long shutdown waits for queued emails. It is
// short on purpose: a process that will not exit because a mail server is
// unreachable is worse than a skipped notification.
const notificationDrainTimeout = 5 * time.Second

// newMailQueue builds the outbound queue and the function that drains it.
//
// The sender reads the settings row on every attempt, so changing the server or
// the password on the page applies to the next message instead of the next
// restart. The queue always exists: whether a message is sent is the settings'
// decision, not a constructor argument.
func newMailQueue(store *mail.Store, logger *slog.Logger) (*mail.Queue, func()) {
	queue := mail.NewQueue(mail.NewSettingsSender(store), 256, logger)
	closeQueue := func() {
		ctx, cancel := context.WithTimeout(context.Background(), notificationDrainTimeout)
		defer cancel()
		if err := queue.Close(ctx); err != nil {
			logger.Warn("drain notification queue", slog.Any("error", err))
		}
	}
	return queue, closeQueue
}

// mailSeed maps the environment's MAIL_* settings onto the row the console owns.
//
// It runs once, on a database that has no settings row yet. After that the page
// is the source of truth and the environment is ignored — otherwise a stale
// MAIL_PASSWORD in a service definition would silently win over the credential
// the operator just typed in.
func mailSeed(cfg config.Mail) mail.Settings {
	seed := mail.DefaultSettings()
	seed.Host = cfg.Host
	if cfg.Port > 0 {
		seed.Port = cfg.Port
	}
	if cfg.TLS != "" {
		seed.TLS = cfg.TLS
	}
	seed.Username = cfg.Username
	seed.Password = cfg.Password
	seed.From = cfg.From
	seed.FromName = cfg.FromName
	seed.AdminTo = mail.ParseRecipients(cfg.Admin)
	// MAIL_ENABLED no longer means anything: there is no master switch any
	// more. The notices keep the defaults (on), and what stops a seeded
	// configuration from mailing anyone is having no SMTP account.
	return seed
}

// newSpamModerator builds the configured filter, reporting whether one exists.
//
// A filter that cannot be constructed is logged and skipped rather than failing
// startup: refusing to serve comments because a third-party key is wrong turns
// an optional feature into an outage.
func newSpamModerator(cfg *config.Config, logger *slog.Logger) (comment.Moderator, bool) {
	switch cfg.Spam.Provider {
	case config.SpamAkismet:
		checker, err := spam.NewAkismet(cfg.Spam.AkismetKey, cfg.Spam.AkismetSiteURL, 10*time.Second)
		if err != nil {
			logger.Error("spam filter disabled", slog.String("provider", "akismet"), slog.Any("error", err))
			return nil, false
		}
		// Verifying at startup puts a rejected key next to the configuration
		// that caused it instead of in a warning on the first comment.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := checker.Verify(ctx); err != nil {
			logger.Warn("akismet key not verified; comments will stay pending until it is fixed",
				slog.Any("error", err))
		}
		logger.Info("spam filter enabled", slog.String("provider", "akismet"))
		return spamModerator{checker: checker, logger: logger, siteURL: cfg.Spam.AkismetSiteURL}, true

	case config.SpamLLM:
		checker, err := spam.NewLLM(cfg.Spam.LLMEndpoint, cfg.Spam.LLMKey, cfg.Spam.LLMModel, cfg.Spam.LLMTimeout)
		if err != nil {
			logger.Error("spam filter disabled", slog.String("provider", "llm"), slog.Any("error", err))
			return nil, false
		}
		logger.Info("spam filter enabled",
			slog.String("provider", "llm"),
			slog.String("model", cfg.Spam.LLMModel))
		return spamModerator{checker: checker, logger: logger, siteURL: cfg.Mail.SiteURL}, true

	default:
		return nil, false
	}
}
