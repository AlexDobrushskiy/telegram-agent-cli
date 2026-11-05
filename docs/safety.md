# Safety model

How telegram-agent-cli keeps an agent from acting on your account, and where that stops.

Five layers, each independent:

1. **MTProto middleware (in the binary).** Every request is checked against an exact allowlist of
   read methods (pinned by a test that walks every request type in gotd, so an upgrade cannot widen
   it) before it is sent. Writes pass only when `TGAC_READONLY` is `0`, `false`, `no` or `off`
   (case-insensitive, surrounding spaces ignored); unset or anything else means read-only. Login
   methods are allowed only inside `auth` subcommands, and approving another device's login
   (`auth.acceptLoginToken`) is never allowed. The commands themselves are gated too: in read-only
   mode `send`, every `auth` command but `status`, and `hook install|uninstall` refuse before
   touching Telegram, the data dir or the settings file. A command is a write unless it is on the
   binary's read list, so a new or renamed one cannot slip through. This is the main barrier; the
   hook's jobs are catching a visible `TGAC_READONLY` override, protecting the data dir, and flagging
   calls off the read list.
2. **Read-only default in the agent's environment.** `hook install` sets `TGAC_READONLY=1` in Claude
   Code's settings, so a write needs an explicit override in the command itself.
3. **The PreToolUse hook.** Before Claude Code runs a tool, `telegram-agent-cli hook claude-pretooluse`
   inspects it (Bash, and Monitor's background commands, the same way) and forces a permission
   prompt ("ask") only for what it can see the call do:
   - run `telegram-agent-cli` with a subcommand off the read list (`send`, `auth` other than `status`,
     `hook install`, unknown ones, or one that comes from an expansion), including through `sudo`,
     `env`, `timeout`, `sh -c`, `bash -lc`, `eval`, `ssh host …`, `watch`, `tmux`, `hyperfine`,
     `find -exec`, `xargs`, `$(…)`, pipes and heredocs fed to a shell, shell scripts it can read, a
     program named by a variable set earlier in the command (`B=./bin/telegram-agent-cli; $B send`),
     or `go run …/cmd/telegram-agent-cli`; an `e2e` write whose `--chat` is not an allow-list entry;
   - set `TGAC_READONLY` to a value that turns read-only off (`0`, `false`, `no`, `off`) or that it
     can't read (`TGAC_READONLY=$X`): as a command prefix, through `env`, `export`/`declare`, `read`,
     `printf -v` or `for`. An empty value keeps read-only on and passes;
   - name a path in a data dir (`$TGAC_HOME` and the default `~/.config/telegram-agent-cli`) as a
     word, a redirect target, or inside a word (`curl -F f=@…/session.json`). Words are normalised
     first (quotes, `~`, `$HOME`, `$XDG_CONFIG_HOME`, variables set earlier, the cwd and `cd`, globs,
     symlinks); a longer name such as `telegram-agent-cli-notes.md` is not the dir;
   - use Read on `config.json`, `session.json`, `peers.json` or `pending-login.json` in the data dir,
     Grep on it or on a directory that contains it, or Edit/Write/MultiEdit/NotebookEdit under it;
   - call a Telegram tool (an MCP server named `telegram`/`tgac`) other than a read.

   Everything else passes, on purpose: interpreter code (`python3 -c`, `node -e`, heredocs fed to an
   interpreter), a program or script named by an expansion, a script that doesn't exist yet,
   decoded or downloaded payloads, recursive reads of `~`, and Claude Code or Codex settings files.
   The hook used to ask about those too; in practice almost every such prompt was a false alarm
   (95% over a week of real agent sessions), and agents started approving prompts automatically.
   A prompt that fires on everything protects nothing, so the hook now asks only when the risk is
   visible, and the binary's write gate covers the rest.

   The name inside an argument or heredoc does not count: `git commit -m "…telegram-agent-cli send…"`,
   `grep telegram-agent-cli README.md`, a `claude -p "…"` prompt or a heredoc written to a file pass.

   How Claude Code handles "ask" (verified 2026-10-28 on Claude Code 2.1.307): interactive sessions
   show the permission prompt in every mode, including `bypassPermissions`; in headless `claude -p`
   runs, where nobody can answer, the call is denied.
4. **Watchers** have their own gate (registry, scopes, rate limits, pause files); see [watchers.md](watchers.md).
5. **The e2e harness** has its own gate too (details [below](#e2e-harness)): it writes only to bots
   the owner put in `e2e.allowed_chats`.

**Limits.** The hook is a second check, not a sandbox:

- Code running as your OS user can read the session file. The hook only sees the tool call, not
  what a program does once it runs.
- Anything the hook can't see passes: interpreter code, `eval "$CMD"`, `"$BIN" send …`, a script
  written by one call and run by the next, `make`/`go test`/`npm run`, a hand-written MTProto client.
  Each of these can send from the account if the session file is readable and `TGAC_READONLY` is
  turned off inside that code; the binary's own write gate is the main barrier.
- The prompt is a question to you, not your approval. In `bypassPermissions` mode it is the only
  thing between the agent and the command, so read each one before approving.

The session file is the real boundary: it is full access to the account. If you run agents you
don't trust, run them as a different OS user or in a container that cannot read the data dir.

## E2E harness

`telegram-agent-cli e2e …` drives the owner's own bot as the owner, for end-to-end tests. It works
in read-only mode, so unattended test agents can use it, and it is fenced like this:

- **Allow-list.** Writes go only to entries of `e2e.allowed_chats` in `config.json`. A `--chat`
  that matches no entry is refused (exit 8, `e2e_refused`) before connecting; a numeric id must
  equal the resolved id of an entry. The target must be a bot user; a person, group or channel is
  refused even if listed.
- **Guard grant.** After that check the command runs its writes under a grant for that one bot.
  The middleware then lets through only `messages.sendMessage`, `messages.sendMedia`,
  `messages.sendReaction`, `messages.getBotCallbackAnswer`, `messages.sendWebViewData`,
  `messages.requestSimpleWebView`, `messages.requestWebView`, `messages.requestMainWebView` and
  `messages.setTyping` (typing, and "stop" on a live draft), each only when every peer it names is
  that bot user, plus `upload.saveFilePart`/`upload.saveBigFilePart` for media. A send must be
  plain: a reply only to a message in that chat (no quote from another chat), no send-as, no
  quick-reply shortcut. Wrapped requests, always-denied methods and write-like arguments (paid
  sends) stay blocked. The allow-list is read again once the account lock is held, so a bot
  disallowed while a command waited for the lock gets nothing.
- **Hook.** `e2e` reads and writes pass without a prompt, since the binary enforces the list. The
  hook still asks when an e2e write names a `--chat` that is not literally an allow-list entry
  (`@name`, `name`, `t.me/name`, case-insensitive), and when it comes from an expansion.
- **Changing the list is a write.** `e2e allow` and `e2e disallow` are off the read list: they need
  `TGAC_READONLY=0` and the hook asks, so an agent cannot add a chat and then send there unprompted.
  Editing `config.json` directly is a data-dir write, which asks too.

## Codex

`telegram-agent-cli hook codex-pretooluse` runs the same checks for Codex's `PreToolUse` hook.
Codex has no "ask" (it treats one as a hook failure and runs the call), so the decision is `deny`
with the reason; run a denied command yourself, outside the agent.

What Codex sends (codex-cli 0.165, captured from `codex exec` and the TUI): every shell call
(`exec_command`, also from code mode) arrives as `{"tool_name":"Bash","tool_input":{"command":"…"}}`;
a patch as `{"tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch…"}}`; an MCP call
as `mcp__<server>__<tool>` with its JSON arguments, or the raw argument string when they don't
parse. `write_stdin` (typing into a running command) and code mode's `wait` send no `PreToolUse`.
The Codex hook makes the same decisions as the Claude Code hook ([what a payload is checked
for](#what-the-hook-reads-in-a-payload)), as `deny`. A shell or REPL started on the terminal passes:
what is typed into it later is not seen, like any other opaque execution.

Add the hook to `~/.codex/hooks.json` with matcher `.*` (it must see every tool), then approve it
with `/hooks`:

```json
{"hooks": {"PreToolUse": [{"matcher": ".*", "hooks": [
  {"type": "command", "command": "/absolute/path/telegram-agent-cli hook codex-pretooluse", "timeout": 10}
]}]}}
```

Set `TGAC_READONLY=1` in Codex's shell environment as well (`[shell_environment_policy]` in
`config.toml`). `hook install` covers Claude Code only.

## Claude Code hook details

`hook install` edits `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR/settings.json`, or
`--settings PATH`). It sets `env.TGAC_READONLY` to `"1"` and adds three `PreToolUse` entries, matcher
`Bash|Monitor`, matcher `Read|Edit|Write|MultiEdit|NotebookEdit|Grep` and matcher
`mcp__.*|PowerShell` (MCP calls get the checks [below](#what-the-hook-reads-in-a-payload)), each running
`<absolute path of this binary> hook claude-pretooluse` with `"timeout": 10` and
`"onFailure": "block"`. That fails closed: if the hook cannot start, crashes or times out, the tool
call is blocked. `onFailure` needs Claude Code 2.1.295 or later; older versions ignore it and let the
call through. The hook itself gives up after 3 seconds and asks instead of waiting.

It keeps every other key and their order, appends to an existing entry with the same matcher instead
of adding a second one, moves the guard out of entries with an older matcher (such as plain `Bash`),
and updates the path if the binary moved. It writes atomically and saves the previous file as
`settings.json.bak-YYYYMMDD-HHMMSS` (with a `-N` suffix if that name exists). When `settings.json`
is a symlink, the link is kept: the target file is rewritten and the backup sits next to the target,
named after it. Running it again changes nothing. `--dry-run` prints the diff and the resulting
settings. Use `--binary PATH` when the binary is reached through a package manager's stable path.
`hook uninstall` removes the entries and `TGAC_READONLY=1`.

For other agent harnesses, the hook reads Claude Code's PreToolUse JSON on stdin
(`{"tool_name":"Bash","tool_input":{"command":"…"},"cwd":"…"}`) and prints
`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"…"}}`
or nothing; it always exits 0. `hook claude-pretooluse --help` prints its own help.

## What the hook reads in a payload

Both hooks (Claude Code: ask, Codex: deny) read a payload by the role of each field:

- **Shell tools** (`Bash`, `PowerShell`, `exec_command`, `shell`, `local_shell`, `container.exec`,
  `shell_command`…, with or without `functions.`): the Bash checks on each `command` and `cmd`
  field (both are read when both are present), string or argv array. `workdir` is the cwd for
  relative paths.
- **File tools** (`Read`, `Write`, `Edit`, `MultiEdit`, `NotebookEdit`, `Grep`): the path only;
  content is data.
- **`apply_patch`**: the files a patch adds, updates, deletes or moves to, resolved against the cwd.
- **Any other tool** (MCP, unknown names): `command`/`cmd` get the Bash checks, at any depth (parallel
  tool uses, a dispatch, JSON-encoded strings); terminal input (`write_stdin`'s `chars`) gets the
  Bash checks; a `TGAC_READONLY` env key that turns read-only off asks; a path field (`path`,
  `file_path`, `destination`…) or patch line inside the data dir asks. A Telegram tool (`telegram` or
  `tgac` in its name, or a dispatch naming such a server) asks unless its name starts with a read
  verb (`get_`, `list_`, `search_`…). `code`/`script` fields are not read. Other text is data.
- **Fail closed on malformed payloads.** A payload that isn't one JSON object, has no tool name,
  carries its input in another envelope (`arguments`, `input`, `parameters`…), or whose
  `tool_input` is not an object asks; so does a shell tool without a string or argv command, a
  runner-named tool (`exec`, `shell`, `eval`…) with neither a command nor code, and a call the
  guard fails to check in time.

Everything else stays silent (see **Limits**).
