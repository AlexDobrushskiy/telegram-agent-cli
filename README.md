# telegram-agent-cli

Safe access to your own Telegram account for AI agents. A single Go binary that talks MTProto as a
logged-in device on your account, prints JSON, and is **read-only unless you switch writes on**.

```console
$ telegram-agent-cli messages search "invoice" --limit 5
{"schema_version":"1","ok":true,"data":{"query":"invoice","messages":[…]}}
```

## Why

Agents (Claude Code, Codex, scripts) are useful for "what did people write me today", "find that
message about the invoice", or "summarize this group". The Telegram **Bot API can't do that**: a bot
only sees chats it was added to, never your private conversations. Reading your chats needs a *user*
session (MTProto), and a user session can do everything you can: send, delete, leave groups, change
your profile. This tool gives agents the reading part and makes everything else deliberate:

- **Read-only by default, enforced on every request.** A gotd middleware checks each MTProto call
  before it leaves the process and refuses anything that is not on an exact allowlist: the 28 read
  calls the tool and its MTProto library make (dialogs, history, search, resolve, media download,
  config) plus pings.
  Allowed calls are still refused when they carry a write-like argument (spending Stars, sharing
  location, counting a view, a referral). Read-*named* calls that act, such as pressing a bot button
  or accepting a gift offer, are not on the list.
- **Every account-changing command is refused in read-only mode** by the binary itself (exit 3,
  before it touches anything): `send`, every `auth` command except `status` (logins, logout, session
  import) and `hook install|uninstall` (except `--dry-run`).
- **Reading leaves no trace.** It never marks messages read, never sets online/typing status, never
  increments channel views.
- **JSON everywhere**, stable exit codes, no prompts except the interactive login.
- **A Claude Code hook** that asks you before any write, read-only override or access to the
  session files (see [Safety model](#safety-model)).
- **Watchers**: an optional, gated path for autonomous replies, off until you register one.

## Install

```sh
go install github.com/AlexDobrushskiy/telegram-agent-cli/cmd/telegram-agent-cli@latest
```

Or download a release archive (macOS and Linux, amd64/arm64) from the
[releases page](https://github.com/AlexDobrushskiy/telegram-agent-cli/releases), check it against
`checksums.txt`, and put `telegram-agent-cli` on your `PATH`.

From source: `make build` (writes `bin/telegram-agent-cli`), `make test`, `make lint`.

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

   Optionally copy [`skills/telegram-agent-cli`](skills/telegram-agent-cli/SKILL.md) to
   `~/.claude/skills/` so agents know how to use the tool.

## Commands

All output is JSON on stdout. `--chat` accepts a marked id (users positive, groups negative,
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
| `messages mentions [--chat X] [--unread] [--limit 30]` | Messages that @-mention or reply to you (`--unread` does not mark them read) |
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

### Login and maintenance (needs `TGAC_READONLY=0`, except `auth status`)

`auth init` · `auth qr [--timeout 3m]` · `auth request-code` · `auth verify --code C [--password-env VAR]` ·
`auth password --password-env VAR` · `auth import-telethon --session PATH` · `auth logout` (terminates
this session). In read-only mode they fail with exit 3 (`"code":"readonly"`) before doing anything.

### Watchers

`watch reply --watcher NAME --chat ID --text TEXT [--reply-to N] [--dry-run]` · `watch list` ·
`watch check --watcher NAME`. See [Watchers](#watchers).

### Hook

`hook claude-pretooluse` (the hook itself, reads stdin) · `hook install [--settings PATH] [--binary PATH] [--dry-run]` ·
`hook uninstall [--settings PATH] [--dry-run]`.

## JSON contract

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

## Safety model

Four layers, each independent:

1. **MTProto middleware (in the binary).** Every request is checked against an exact allowlist of
   read methods (pinned by a test that walks every request type in gotd, so an upgrade cannot widen
   it) before it is sent. Writes pass only when `TGAC_READONLY` is exactly `0`, `false`, `no` or `off`;
   unset or anything else means read-only. Login methods are allowed only inside `auth`
   subcommands, and approving another device's login (`auth.acceptLoginToken`) is never allowed.
   The commands themselves are gated too: in read-only mode `send`, every `auth` command but
   `status`, and `hook install|uninstall` refuse before touching Telegram, the data dir or the
   settings file. A command is a write unless it is on the binary's read list, so a new or renamed
   one cannot slip through. The hook therefore only needs to catch the `TGAC_READONLY` override.
2. **Read-only default in the agent's environment.** `hook install` sets `TGAC_READONLY=1` in Claude
   Code's settings, so a write needs an explicit override in the command itself.
3. **The PreToolUse hook.** Before Claude Code runs a tool, `telegram-agent-cli hook claude-pretooluse`
   inspects it and forces a permission prompt ("ask") when the call:
   - runs `telegram-agent-cli` with a subcommand off the read list (`send`, `auth` other than `status`,
     `hook install`, unknown ones), including through `sudo`, `env`, `timeout`, `xargs`, `sh -c`,
     `bash -lc`, `eval`, `ssh host …`, `find -exec`, `$(…)`, pipes, heredocs fed to a shell, shell
     scripts it can read, or `go run …/cmd/telegram-agent-cli`;
   - sets `TGAC_READONLY` to a false, empty or computed value, or mentions `TGAC_HOME`;
   - references the data dir (absolute, `~`, `$HOME` forms) or mixes an MTProto library (Telethon,
     gotd, Pyrogram…) with a session file;
   - uses Read on `config.json`, `session.json`, `peers.json` or `pending-login.json` in the data dir,
     Grep inside it, or Edit/Write/MultiEdit/NotebookEdit anywhere under it.

   The name inside an argument does not count: `git commit -m "…telegram-agent-cli send…"`,
   `grep telegram-agent-cli README.md` or a `claude -p "…"` prompt pass silently. In non-interactive
   runs, where nobody can answer the prompt, the call does not run.
4. **Watchers** have their own gate (registry, scopes, rate limits, pause files); see below.

**Limits.** The hook is a second check, not a sandbox. An agent with shell access can obfuscate a
command, write its own MTProto client and point it at the session file, or edit files the hook
cannot see. The session file is the real boundary: it is full access to the account. If you run
agents you don't trust, run them as a different OS user or in a container that cannot read the
data dir. The prompt the hook shows is a question to you, not your approval; approve only commands
you have read.

## Watchers

A watcher is a long-running agent session allowed to answer certain incoming messages on its own,
for example a support helper that replies to people who write first. It is the only autonomous send
path, and nothing is allowed until you add an entry to `watchers.json` in the data dir yourself
(edit it by hand; the hook asks before an agent writes there):

```json
{
  "helper": {
    "session": "the-watcher-session-id",
    "chats": {"123456789": "customer who wrote first"},
    "scope": "dm-first",
    "groups": false,
    "mentions_only": true,
    "max_chars": 300,
    "per_hour": 20,
    "per_chat_seconds": 20,
    "backoff_minutes": 15,
    "dir": "/path/to/helper-workdir",
    "approved": "who approved this and when"
  }
}
```

Everything a watcher is trusted with lives in this entry, which only you edit: `session` is the id
the calling session must present (env `TGAC_WATCHER_SESSION`, configurable), `chats` is the
allowlist (chat id → note). The optional `dir` is the watcher's own workdir; a `PAUSE` file there
pauses it, and the watcher may keep notes there, but nothing in it grants anything. Every send
attempt is logged to `<data dir>/watchers/NAME/sends.jsonl`, where a watcher can't rewrite its own
rate history; `<data dir>/watchers/NAME/PAUSE` also pauses it. `watch reply` works in read-only
mode (the guard lets exactly that one `messages.sendMessage` through) and refuses (exit 7, reason
in `error.message`, a stable id in `error.details.rule`) unless:

- no `PAUSE-ALL` in the data dir, the watcher is registered with a valid entry, no `PAUSE` for it;
- the caller's session id equals the entry's `session` (refusals never show either value);
- the text is 1…`max_chars` characters, and the per-watcher (`per_hour`: its log or its ledger rows,
  whichever is larger), global (`global_per_hour`: all watcher logs or the ledger) and per-chat
  (`per_chat_seconds`) limits hold;
- the chat is a 1:1 with a person (not a bot, not Saved Messages) that fits the scope: `listed` = in
  `chats`; `contacts` = a mutual contact or listed; `dm-first` = they wrote first (for flows that
  answer new people); or a group (never a broadcast channel) with `"groups": true` that is listed;
- 1:1: the latest message is theirs and no watcher answered it yet; you haven't written in the chat
  within `backoff_minutes` (watcher replies, known from `answered.jsonl`, don't count);
- group: `--reply-to` is required; the target is not yours, not in `answered.jsonl`, and no message
  of yours replies to it; with `mentions_only` it mentions you or replies to you; you haven't written
  in the group within `backoff_minutes`;
- a chat too busy to verify these rules from its recent history is refused, not guessed.

Checks and the send run under a lock in the data dir, so two watchers cannot answer one message.
The registry is re-read after taking that lock and again right before sending: removing the entry,
changing `session`, delisting the chat or adding a `PAUSE` stops a reply already in flight.
`--dry-run` runs every check and reports what it would send. `watch list` and `watch check --watcher
NAME` validate the registry offline and show only whether a session binding is set. Stop
everything at once with `touch <data dir>/PAUSE-ALL`.

A typical loop: `messages wait-new --state state/wm.json --dms` → the agent drafts a reply →
`watch reply --watcher helper --chat ID --text "…"`.

## Telegram terms and ban risk

Using your own `api_id` with a third-party client is allowed by Telegram's
[API Terms of Service](https://core.telegram.org/api/terms). What gets accounts limited or banned is
behaviour that looks like spam or abuse: bulk or cold messaging, adding many contacts, joining many
groups, sending at machine speed, or hammering the API. Keep sends human-paced and in conversations
people started, respect `FLOOD_WAIT` (the tool returns it as exit 5 with `retry_after`; wait, don't
loop), and expect Telegram to terminate sessions it considers suspicious. You are responsible for
what your agents send from your account.

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
| `TGAC_READONLY` | `0`/`false`/`no`/`off` enables writes; anything else, or unset, is read-only |
| `TGAC_LOCK_WAIT` | seconds to wait for the account lock (default 90) |
| `TGAC_WATCHER_SESSION` | the calling agent session's id, for `watch reply` |
| `CLAUDE_CONFIG_DIR` | where `hook install` finds `settings.json` (default `~/.claude`) |

## Claude Code hook details

`hook install` edits `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR/settings.json`, or
`--settings PATH`): it sets `env.TGAC_READONLY` to `"1"` and adds two `PreToolUse` entries, matcher
`Bash` and matcher `Read|Edit|Write|MultiEdit|NotebookEdit|Grep`, each running
`<absolute path of this binary> hook claude-pretooluse`. It keeps every other key, keeps their order,
appends to an existing `Bash` entry instead of adding a second one, updates the path if the binary
moved, writes atomically, and saves the previous file as `settings.json.bak-YYYYMMDD-HHMMSS`.
Running it again changes nothing. `--dry-run` prints the diff and the resulting settings. Use
`--binary PATH` when the binary is reached through a package manager's stable path. `hook uninstall`
removes the entries and `TGAC_READONLY=1`.

For other agent harnesses, the hook reads Claude Code's PreToolUse JSON on stdin
(`{"tool_name":"Bash","tool_input":{"command":"…"},"cwd":"…"}`) and prints
`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"…"}}`
or nothing; it always exits 0.

## License

MIT, see [LICENSE](LICENSE). Security issues: see [SECURITY.md](SECURITY.md).
