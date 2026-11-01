# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `e2e`: an end-to-end test harness for your own bots. Send text, photos, voice notes, files and
  locations; reply, react, press inline and keyboard buttons; send Mini App data; open Mini App URLs
  with signed initData; stop live drafts; read messages, the reply keyboard and the pinned message;
  wait for replies, edits, reactions, pins, keyboards and drafts. Writes go only to bots in
  `e2e.allowed_chats` (`e2e allow`), work in read-only mode through a per-bot guard grant, and need
  no hook prompt; other targets exit 8, waits that time out exit 9.
- `hook codex-pretooluse`: the guard for Codex (denies where Claude Code would ask); Codex hook
  configs are protected like Claude Code settings.

## [0.1.3] - 2026-10-29

### Added

- `brew install AlexDobrushskiy/tap/telegram-agent-cli` (Homebrew cask, published on each release).
- `install.sh`: one-line install of the latest release on Linux or macOS, with checksum check.
- `docs/agent-setup.md`: a setup guide an agent can follow; the README quick start is one prompt.

## [0.1.2] - 2026-10-29

### Changed

- Shorter README; details moved to `docs/safety.md`, `docs/watchers.md` and `docs/reference.md` (also shipped in release archives).

## [0.1.1] - 2026-10-29

### Added

- README demo (recorded with a fictional demo backend, build tag `demo`).

### Fixed

- Releases are published, not left as drafts.
- `auth qr --timeout`, `messages wait-new --timeout` and `--interval` accept seconds or a duration (`300`, `5m`).

## [0.1.0] - 2026-10-29

### Added

- MTProto user-account client (gotd) with a read-only middleware: every request is checked against
  an exact allowlist of read methods (and write-like arguments) before it is sent; writes need
  `TGAC_READONLY=0|false|no|off` (case-insensitive, trimmed).
- Reading never marks read, never sets online/typing status, never increments channel views.
- Account-changing commands (`send`, every `auth` command except `status`, `hook install|uninstall`
  without `--dry-run`) are refused in read-only mode by the binary itself, with exit 3.
- JSON envelope (`schema_version` 1) and stable exit codes for every command.
- Login: `auth init`, `auth qr`, `auth request-code` / `auth verify` / `auth password`,
  `auth import-telethon`, `auth status`, `auth logout`.
- Read commands: `chats list|show`, `messages list|search|show|context|mentions|wait-new`,
  `contacts search`, `media download`, `me`, `doctor`, `version`.
- Write commands: `send text`, `send file`.
- Watchers: `watch reply` (registry, scopes, group mode, rate limits, shared answered ledger, pause
  files, dry run), `watch list`, `watch check`.
- Claude Code guard: `hook claude-pretooluse`, `hook install`, `hook uninstall`.
- Agent skill in `skills/telegram-agent-cli/`.
- Release builds for macOS and Linux (amd64, arm64).

[0.1.0]: https://github.com/AlexDobrushskiy/telegram-agent-cli/releases/tag/v0.1.0
