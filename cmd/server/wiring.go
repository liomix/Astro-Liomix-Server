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

// newSpamModerator builds the built-in filter, reporting whether it is on.
//
// The filter has no key and no network, so there is nothing to verify and
// nothing that can fail at startup: an enabled filter either runs, or the tuning
// was invalid and Load has already rejected it.
func newSpamModerator(cfg *config.Config, logger *slog.Logger) (comment.Moderator, bool) {
	if !cfg.Spam.Enabled {
		return nil, false
	}

	options := []spam.Option{}
	if cfg.Spam.LexiconFile != "" {
		groups, err := spam.LoadLexiconFile(cfg.Spam.LexiconFile)
		if err != nil {
			logger.Warn("spam word list not used, keeping the built-in one",
				slog.String("path", cfg.Spam.LexiconFile),
				slog.Any("error", err))
		} else {
			options = append(options, spam.WithLexicon(groups))
			logger.Info("spam word list loaded",
				slog.String("path", cfg.Spam.LexiconFile),
				slog.Int("groups", len(groups)))
		}
	}

	tuning := spam.DefaultConfig()
	tuning.Threshold = cfg.Spam.Threshold
	tuning.Window = cfg.Spam.Window
	tuning.MaxPerIP = cfg.Spam.MaxPerIP
	tuning.MaxPerEmail = cfg.Spam.MaxPerEmail

	logger.Info("spam filter enabled",
		slog.Int("threshold", tuning.Threshold),
		slog.Duration("window", tuning.Window),
		slog.Int("max_per_ip", tuning.MaxPerIP),
		slog.Int("max_per_email", tuning.MaxPerEmail))
	options = append(options, spam.WithConfig(tuning))
	return spamModerator{checker: spam.New(options...), logger: logger}, true
}
