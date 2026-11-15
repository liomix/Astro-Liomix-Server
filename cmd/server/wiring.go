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
// The queue always exists, but it only talks to SMTP when mail is enabled: a
// caller never has to test for nil, and a disabled deployment still has the
// same code path.
func newMailQueue(cfg *config.Config, logger *slog.Logger) (*mail.Queue, func(), error) {
	if !cfg.Mail.Enabled {
		return mail.NewQueue(mail.NoopSender{}, 16, logger), func() {}, nil
	}

	sender, err := mail.NewSMTPSender(mail.SMTPConfig{
		Host:     cfg.Mail.Host,
		Port:     cfg.Mail.Port,
		TLS:      cfg.Mail.TLS,
		Username: cfg.Mail.Username,
		Password: cfg.Mail.Password,
		From:     cfg.Mail.From,
		FromName: cfg.Mail.FromName,
	})
	if err != nil {
		return nil, nil, err
	}

	queue := mail.NewQueue(sender, 256, logger)
	closeQueue := func() {
		ctx, cancel := context.WithTimeout(context.Background(), notificationDrainTimeout)
		defer cancel()
		if err := queue.Close(ctx); err != nil {
			logger.Warn("drain notification queue", slog.Any("error", err))
		}
	}
	return queue, closeQueue, nil
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
