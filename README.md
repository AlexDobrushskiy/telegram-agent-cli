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

Paste this into your agent (Claude Code, Codex, …):

> Set up telegram-agent-cli for me by following
> https://github.com/AlexDobrushskiy/telegram-agent-cli/blob/main/docs/agent-setup.md

It installs the tool, walks you through getting API keys and scanning a login QR code, and adds the
guard hook. You run the two login commands in your own terminal, so your keys never pass through
the agent. Prefer doing it by hand? See [manual setup](docs/reference.md#setup).

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
