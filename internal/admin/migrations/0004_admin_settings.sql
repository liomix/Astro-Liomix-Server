-- Operator preferences that belong to the console rather than to a deployment.
--
-- They live in the database because changing them must not require a restart:
-- "stop emailing me" is a decision made while looking at the page, and an
-- environment variable cannot be changed from there. Secrets do not belong
-- here — SMTP credentials stay in the environment.
CREATE TABLE IF NOT EXISTS admin_settings (
    key        text PRIMARY KEY,
    value      text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
