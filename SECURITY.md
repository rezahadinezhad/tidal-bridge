# Security boundaries

Tidal Bridge executes trusted developer code on devices the user explicitly approved. It is not a hostile-code sandbox. Pairing creates an independent random bearer secret per worker; ADB authorization alone does not add an unpaired phone. Host and worker APIs bind only to loopback, and USB tunnels are scoped to serials. The dashboard uses the host secret with a restrictive content-security policy. Never publish dashboard or bootstrap URLs or token files.

## The ADB-shell worker runs with the shell user's rights

The fast worker runs as Android's `shell` user, the identity behind `adb shell`, not inside an app sandbox. It exists because Android confines app processes to efficiency cores and may kill them. The shell user can do more than an app:

- read shared storage (`/sdcard`: photos, downloads, documents);
- inject input (`input`), take screenshots (`screencap`) and read system logs;
- read system state through `dumpsys`, which includes notification content;
- install packages.

Jobs run in a Debian userland through `proot`, with only the worker directory bound in, but `proot` translates paths and is not a security boundary. A malicious dependency or test that runs on the phone could use those rights. Run only code you would run on the laptop itself, and prefer the Termux worker when that tradeoff is not acceptable. The worker directory `/data/local/tmp/tidalbridge` is mode 0700, so other apps cannot read it. Its token file is mode 0600. Other apps can connect to the worker's loopback port, but every request needs the token.

The Termux worker runs inside Termux's app sandbox: it can read Termux's own files and nothing private to other apps.

## What leaves the laptop

Default sync excludes `.git`, dependency directories, build output, logs, credentials, keys and other common private paths; files named like credentials are excluded wherever they appear. `.env` files are synced only when a project sets `sync_env_files`. Use that setting only when those files hold no secrets the phone should have. `.tidalbridgeignore` adds exclusions and can bring back paths a `.gitignore` excludes (`!dir/`). Review what a re-include brings in, because filename filters cannot detect a secret inside an ordinary file. Symlinks, traversal, output escapes, malformed IDs and hashes, protocol or identity mismatches, and credential-named environment overrides are rejected. Only declared output files are copied back.

Remote jobs receive a small environment allow-list rather than agent credentials. Provisioning needs explicit approval, installs from lockfiles or pinned requirements, and disables package install scripts unless a task allows them. A lockfile does not make Windows native code portable.

## Laptop side

Command adapters act only in approved projects and in agent sessions configured to use them; system and user PATH are unchanged. Local runs keep the usual environment, with a recursion guard. Windows runtime paths are stripped before remote submission. Worker sync excludes `.codex` and `.claude` directories. The job object used to measure local commands only accounts; it sets no limits.

Timeout and cancellation terminate the process group or tree. Durable request IDs prevent duplicate submission; non-idempotent work is never replayed automatically. Side effects outside the worker's copy remain the caller's responsibility.

## Other limits

Neither worker can enforce network denial; requests that require it fail. The project never requests root, never disables Android security verification, and never changes system or security settings. State and secrets stay outside the repository, in `%USERPROFILE%\.tidalbridge`. Protect backups of it accordingly. Audit logs can include command arguments, paths, outputs and errors, so do not pass secrets in argv. No cloud telemetry is sent by Tidal Bridge. Tools that jobs run may send their own, for example Next.js telemetry, unless disabled.

## Universal mode

Universal mode runs detected checks and tests of every project on the phone, so more third-party package code runs there under the shell user (inside `proot`, which is not a security boundary). `.env` files never leave the laptop unless a project is listed with `tidalbridge automation share-env`; key and credential files never do. Exclude a project with `tidalbridge automation exclude --workspace PATH`, or turn the mode off with `tidalbridge automation universal off`.
