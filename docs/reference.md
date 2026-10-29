# Reference

## Install

| How | Command |
|---|---|
| Homebrew (macOS) | `brew install AlexDobrushskiy/tap/telegram-agent-cli` |
| Script (Linux, macOS) | `curl -fsSL https://raw.githubusercontent.com/AlexDobrushskiy/telegram-agent-cli/main/install.sh \| sh` installs the latest release to `~/.local/bin` after checking its checksum. `VERSION=0.1.3` pins a version, `INSTALL_DIR=…` changes the target |
| Go 1.25+ | `go install github.com/AlexDobrushskiy/telegram-agent-cli/cmd/telegram-agent-cli@latest` (binary in `$(go env GOPATH)/bin`) |
| Manual | download an archive for your OS/CPU from [releases](https://github.com/AlexDobrushskiy/telegram-agent-cli/releases), check it against `checksums.txt`, put `telegram-agent-cli` on your `PATH` |
| Source | `make build` writes `bin/telegram-agent-cli` |

## Setup

Setup changes your account and settings, so each step that writes runs with writes switched on for
that one command (`TGAC_READONLY=0`); everything else stays read-only.

1. **Create an API app.** Sign in at <https://my.telegram.org> with your phone number, open
   **API development tools**, and create an application (any name; platform "Desktop" is fine).
   Note the `api_id` (a number) and `api_hash` (32 hex characters). They identify your app, not
   your account; keep the hash private.

2. **Store the credentials.** The hash is read from stdin so it never lands in shell history or
   process lists:

   ```sh
   TGAC_READONLY=0 telegram-agent-cli auth init --api-id 1234567 --phone +15550000000
   # api_hash: (paste, no echo)
   ```

3. **Log in**, one of:

   - **QR (recommended).** `TGAC_READONLY=0 telegram-agent-cli auth qr` shows a QR code in the terminal. On your
     phone open Telegram → **Settings → Devices → Link Desktop Device** and scan it. If the account
     has a two-step verification password, you are asked for it in the terminal.
   - **Login code.** `TGAC_READONLY=0 telegram-agent-cli auth request-code` sends a code to your
     Telegram app, then `TGAC_READONLY=0 telegram-agent-cli auth verify --code 12345`. With
     two-step verification, put the password in an environment variable and pass its name:
     `auth verify --code 12345 --password-env TG_2FA` (or run `auth password --password-env TG_2FA`
     when `verify` returns `"status":"password_needed"`), with `TGAC_READONLY=0` as above.
   - **Import a Telethon session** you already have:
     `TGAC_READONLY=0 telegram-agent-cli auth import-telethon --session ~/path/to/name.session` (reads the auth key
     from Telethon's SQLite file, then checks it with `getMe`; `--force` overwrites an existing
     session, `--no-verify` skips the check).

4. **Check it:** `telegram-agent-cli doctor` reports config, file modes, `authorized` and `readonly`.
   The session shows up in Telegram under Settings → Devices as `telegram-agent-cli`; terminating it
   there logs the tool out.

5. **Guard your agent** (Claude Code):

   ```sh
   telegram-agent-cli hook install --dry-run                  # show the change
   TGAC_READONLY=0 telegram-agent-cli hook install
   ```

   Optionally copy [`skills/telegram-agent-cli`](../skills/telegram-agent-cli/) to
   `~/.claude/skills/` so agents know how to use the tool.

## Commands

Data commands print one JSON object on stdout (help is plain text; `hook claude-pretooluse` uses Claude Code's hook format). `--chat` accepts a marked id (users positive, groups negative,
channels and supergroups `-100…`), `@username`, a `t.me` link, `+phone`, or a dialog name (exact
match first, then substring; an ambiguous name fails and lists the candidates). Prefer ids in
scripts. Dates are ISO 8601, UTC.

### Read

| Command | What it does |
|---|---|
| `chats list [--limit 30] [--unread] [--archived] [--type user\|bot\|group\|supergroup\|channel\|forum] [--query TEXT]` | Dialogs with unread/mention counts and the last message |
| `chats show --chat X` | One chat: type, username, members/about where available |
| `messages list --chat X [--limit 50] [--asc] [--after DATE] [--before DATE] [--min-id N] [--max-id N] [--offset-id N] [--topic N]` | Messages of one chat, newest first; page older with `data.next_offset_id` |
| `messages search QUERY [--chat X] [--from Y] [--limit 30] [--after DATE] [--before DATE]` | Full-text search, account-wide without `--chat` |
| `messages show --chat X --id N[,N…]` | Specific messages |
| `messages context --chat X --id N [--around 8]` | A message with its neighbours |
| `messages mentions [--chat X] [--unread] [--limit 30] [--scan 50]` | Messages that @-mention or reply to you (`--unread` does not mark them read). Without `--chat` or `--unread` it searches your `--scan` most recent groups |
| `messages wait-new --state FILE [--chats ID,…] [--dms] [--groups] [--mentions] [--timeout 600] [--interval 30] [--since DATE]` | Block until new incoming messages arrive after the watermark in FILE, return them, advance the watermark. First run records the watermark and returns `[]`. Empty list on timeout |
| `contacts search QUERY [--limit 20]` | Your contacts and chats (`mine`), then Telegram's public directory (`public`) |
| `media download --chat X --id N --output DIR` | Save a message's photo/voice/file; returns the path |
| `me` · `doctor` · `version` · `auth status` | Account, health, build, login state |

```sh
telegram-agent-cli chats list --unread --limit 20
telegram-agent-cli messages list --chat @example_user --limit 20 --asc
telegram-agent-cli messages search "flight" --after 2026-01-01
telegram-agent-cli messages context --chat -1001234567890 --id 4242 --around 5
telegram-agent-cli media download --chat 123456789 --id 77 --output /tmp/tg-media
```

### Write (needs `TGAC_READONLY=0`)

| Command | |
|---|---|
| `send text --chat X --message TEXT [--reply-to N]` | Send a message |
| `send file --chat X --path FILE [--caption TEXT] [--reply-to N]` | Send a file |

In read-only mode these fail with exit 3 (`"code":"readonly"`) before connecting. Run them only with
the account owner's OK on the exact text: `TGAC_READONLY=0 telegram-agent-cli send text --chat 123 --message "…"`.

### Login and maintenance

All need `TGAC_READONLY=0`, except `auth status`.


`auth init` · `auth qr [--timeout 3m]` · `auth request-code` · `auth verify --code C [--password-env VAR]` ·
`auth password --password-env VAR` · `auth import-telethon --session PATH` · `auth logout` (terminates
this session). In read-only mode they fail with exit 3 (`"code":"readonly"`) before doing anything.

### Watchers

`watch reply --watcher NAME --chat ID --text TEXT [--reply-to N] [--dry-run]` · `watch list` ·
`watch check --watcher NAME`. See [watchers.md](watchers.md).

### Hook

`hook claude-pretooluse` (the hook itself, reads stdin) · `hook install [--settings PATH] [--binary PATH] [--dry-run]` ·
`hook uninstall [--settings PATH] [--dry-run]`.

## JSON output

Success:

```json
{"schema_version": "1", "ok": true, "data": {"messages": [{"id": 4242, "chat_id": 123456789, "chat_name": "Example User",
  "date": "2026-05-01T09:30:00Z", "from_me": false, "sender_id": 123456789, "sender_name": "Example User",
  "text": "see you at 10", "reply_to": 4240, "media": {"type": "photo", "size": 183211}}]}}
```

Message fields: `id`, `chat_id`, `chat_name`, `date`, `from_me`, `sender_id`, `sender_name`, `text`,
and when present `edited`, `reply_to`, `forwarded_from`, `media{type,name,mime,size,url,title,question}`,
`album`, `service`, `views`, `mentioned`. Chats: `id`, `name`, `type`, `username`, `unread`,
`mentions`, `pinned`, `archived`, `muted`, `last{id,date,from_me,text,media}`.

Failure:

```json
{"schema_version": "1", "ok": false, "error": {"code": "flood_wait", "message": "telegram: FLOOD_WAIT, retry in 30 s", "details": {"retry_after": 30}}}
```

| Exit | `error.code` | Meaning |
|---|---|---|
| 0 | | ok |
| 1 | `internal` | unexpected error |
| 2 | `usage` | bad flags or arguments |
| 3 | `auth`, `readonly` | not logged in, or a write in read-only mode |
| 4 | `not_found`, `ambiguous` | chat/message not found, or a name matched several chats |
| 5 | `telegram`, `flood_wait` | Telegram RPC error or rate limit (`details.retry_after` seconds) |
| 6 | `lock_busy` | another call held the account lock longer than `TGAC_LOCK_WAIT` |
| 7 | `watcher_refused` | a watcher rule refused the reply (reason in the message) |

`schema_version` changes only on breaking changes to these shapes.

## Configuration

Data dir: `$TGAC_HOME`, default `~/.config/telegram-agent-cli/` (mode 0700, files 0600):

| File | |
|---|---|
| `config.json` | app credentials and device info (below) |
| `session.json` | the MTProto session: **full account access** |
| `peers.json` | cache of chat access hashes |
| `pending-login.json` | between `auth request-code` and `auth verify` |
| `.lock` | one process talks to Telegram at a time |
| `watchers.json`, `answered.jsonl`, `PAUSE-ALL` | watcher registry, shared ledger, global stop |

`config.json` keys:

| Key | Default | |
|---|---|---|
| `api_id`, `api_hash` | | from my.telegram.org |
| `phone` | | international format, used by code login |
| `device_model` | `telegram-agent-cli` | shown in Settings → Devices |
| `system_version` | OS name | |
| `app_version` | binary version | |
| `lang_code` | `en` | |
| `watcher_session_env` | `TGAC_WATCHER_SESSION` | env var naming the calling agent session |
| `global_per_hour` | `30` | watcher sends per hour across all watchers |

Environment:

| Variable | |
|---|---|
| `TGAC_HOME` | data dir |
| `TGAC_READONLY` | `0`/`false`/`no`/`off` (case-insensitive, spaces trimmed) enables writes; anything else, or unset, is read-only |
| `TGAC_LOCK_WAIT` | seconds to wait for the account lock (default 90) |
| `TGAC_WATCHER_SESSION` | the calling agent session's id, for `watch reply` |
| `CLAUDE_CONFIG_DIR` | where `hook install` finds `settings.json` (default `~/.claude`) |
