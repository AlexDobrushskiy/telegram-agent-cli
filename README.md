# telegram-agent-cli

Let your AI agent catch up on your Telegram and find messages, with read-only access by default.

A single Go binary that connects to your personal Telegram account as another device and returns
JSON. Agents such as Claude Code or Codex can use it to answer "What did people send me today?" or
"Find the message about that invoice."

![Demo: chats, search and mentions as JSON; a refused send; the hook asking before a send; a watcher dry-run](docs/demo.gif)

<sub>Recorded with fictional data using an in-memory demo backend.</sub>

## Install

macOS:

```sh
brew install AlexDobrushskiy/tap/telegram-agent-cli
```

Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/AlexDobrushskiy/telegram-agent-cli/main/install.sh | sh
```

Other ways (Go, manual download): [docs/reference.md](docs/reference.md#install).

## Quick start

Run these yourself in a terminal, with your phone signed in to Telegram.

1. **Get API keys.** At <https://my.telegram.org> → **API development tools**, create an app (any
   name). You get an `api_id` and an `api_hash`.
2. **Save them.** Use your own API ID and phone number, then paste the `api_hash` at the hidden
   prompt:
   ```sh
   TGAC_READONLY=0 telegram-agent-cli auth init --api-id 1234567 --phone +15550000000
   ```
3. **Log in.** Run this, then on your phone open Telegram → **Settings → Devices → Link Desktop
   Device** and scan the QR code. If you use two-step verification, type the password when asked.
   ```sh
   TGAC_READONLY=0 telegram-agent-cli auth qr
   ```
4. **Check:** `telegram-agent-cli doctor` should show `"authorized":true`.
5. **Claude Code only:** run `TGAC_READONLY=0 telegram-agent-cli hook install` to add the guard
   hook. Optionally copy the [`skills/telegram-agent-cli`](skills/telegram-agent-cli/) directory
   into `~/.claude/skills/`.

`TGAC_READONLY=0` switches writes on for that one command. Without it, the tool refuses anything
that changes your account. Other ways to log in (a login code, or importing a Telethon session) are
in the [setup reference](docs/reference.md#setup).

## Use

In an agent session with terminal access, try:

> Use `telegram-agent-cli` to summarize my unread Telegram chats. Start with
> `telegram-agent-cli --help`. Tell me which messages need a reply, with chat names and message IDs.

The agent runs commands like these and reads their JSON:

```sh
telegram-agent-cli chats list --unread                  # chats with unread messages
telegram-agent-cli messages list --chat @someone --limit 20
telegram-agent-cli messages search "invoice"            # across all chats
telegram-agent-cli messages mentions --unread           # unread mentions and replies to you
```

Data commands print one JSON object (`schema_version`, `ok`, then `data` or `error`), and failures
use documented exit codes. Reading never marks messages as read and never shows you as online.
All commands, flags, JSON fields and settings are in [docs/reference.md](docs/reference.md).

## Safety

- **Read-only by default.** The binary blocks every Telegram operation outside its read allowlist
  before it leaves your machine. Sending and account changes need `TGAC_READONLY=0`. Watcher
  replies go through their own gate.
- **A guard for Claude Code.** The hook asks you before an agent sends, switches read-only off, or
  touches the session files. It inspects tool calls; it is a second check, not a sandbox.
- **The session file is the boundary.** It gives full access to your account. Run agents you don't
  trust as another OS user.

How it works and where it stops: [docs/safety.md](docs/safety.md).

## Watchers

Watchers let an agent reply on its own, within rules you register yourself: which chats, rate
limits, a dry-run mode and a pause switch. Your agent runs the loop and the CLI checks each reply.
See [docs/watchers.md](docs/watchers.md).

## Development

`make build`, `make test`, `make lint`. Re-record the demo with `vhs demo/demo.tape`.

## License

MIT, see [LICENSE](LICENSE). Security issues: [SECURITY.md](SECURITY.md).
