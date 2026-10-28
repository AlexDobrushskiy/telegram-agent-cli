# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow [Semantic Versioning](https://semver.org/).

## [0.1.0] - Unreleased

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
