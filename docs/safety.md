# Safety model

How telegram-agent-cli keeps an agent from acting on your account, and where that stops.

Four layers, each independent:

1. **MTProto middleware (in the binary).** Every request is checked against an exact allowlist of
   read methods (pinned by a test that walks every request type in gotd, so an upgrade cannot widen
   it) before it is sent. Writes pass only when `TGAC_READONLY` is `0`, `false`, `no` or `off`
   (case-insensitive, surrounding spaces ignored); unset or anything else means read-only. Login
   methods are allowed only inside `auth` subcommands, and approving another device's login
   (`auth.acceptLoginToken`) is never allowed. The commands themselves are gated too: in read-only
   mode `send`, every `auth` command but `status`, and `hook install|uninstall` refuse before
   touching Telegram, the data dir or the settings file. A command is a write unless it is on the
   binary's read list, so a new or renamed one cannot slip through. This is the main barrier; the
   hook's jobs are catching the `TGAC_READONLY` override, protecting the data dir and the Claude Code
   settings, and flagging calls off the read list.
2. **Read-only default in the agent's environment.** `hook install` sets `TGAC_READONLY=1` in Claude
   Code's settings, so a write needs an explicit override in the command itself.
3. **The PreToolUse hook.** Before Claude Code runs a tool, `telegram-agent-cli hook claude-pretooluse`
   inspects it (Bash, and Monitor's background commands, the same way) and forces a permission
   prompt ("ask") when the call:
   - runs `telegram-agent-cli` with a subcommand off the read list (`send`, `auth` other than `status`,
     `hook install`, unknown ones), including through `sudo`, `env` (also `-S`/`--split-string`),
     `timeout`, `script`, `busybox`, `sh -c`, `bash -lc`, `eval`, `ssh host [--] …`, `watch`, `tmux`,
     `hyperfine`, `find -exec`, shell functions, `$(…)`, pipes, heredocs fed to a shell, shell scripts it
     can read, or `go run …/cmd/telegram-agent-cli`. `xargs` without `-I` and GNU `parallel` append
     their input, so the fixed part must already be a full read command (`xargs telegram-agent-cli
     messages show --chat 1 --id` passes, `xargs telegram-agent-cli` asks);
   - runs it from interpreter code (`python -c`, `node -e`, `ruby`/`perl -e`, `php -r`, `awk`,
     `osascript`, heredocs, script files): string literals after the name are its argv
     (`['telegram-agent-cli', 'auth', 'logout']`), a literal holding a whole command line is parsed
     as shell, and a call the hook can't resolve (a variable, concatenation, `%`-format) asks;
   - runs content it can't see (a pipe into a shell or interpreter from anything but literal
     `echo`/`printf`/`cat`, `bash <(…)`, `source <(…)`, `sh -c "$(…)"`, `eval "$(…)"`, a script named
     by a variable, a script that isn't a readable regular file) when that content is decoded on the
     way in (`base64 -d`, `xxd -r`, `openssl enc -d`, `uudecode`, `rev`, `tr`), or when the visible
     command also names the binary, a `TGAC_` variable, the watcher variable, the data dir or an
     MTProto library. Otherwise `curl … | bash`, `eval "$(ssh-agent -s)"` and `bash "$SCRIPT"` pass,
     since the hook runs on every Bash call, not only Telegram ones;
   - sets `TGAC_READONLY` to a false, empty or computed value in any form: `export
     TGAC_READ''ONLY=0`, `read`/`printf -v`/`for`/`declare -n`, `${TGAC_READONLY:=0}`, or code such as
     `os.environ['TGAC_READONLY'] = str(0)` and `$ENV{TGAC_READONLY}=0` (only a plain literal that
     stays read-only, such as `'1'`, passes); also a `TGAC_` name built from parts;
   - sets or exports the watcher session variable (`TGAC_WATCHER_SESSION`, or `watcher_session_env`
     from the data dir's `config.json`);
   - sets or reads `TGAC_HOME` (`TGAC_HOME=…`, `$TGAC_HOME`, `os.environ['TGAC_HOME']`); a bare
     mention is fine;
   - references a data dir: `$TGAC_HOME` and the default `~/.config/telegram-agent-cli`. Each word is
     normalised first (quote removal, `~`, `$HOME`, `${HOME}`, `$XDG_CONFIG_HOME`, variables set
     earlier in the command, the cwd and `cd`, `.`/`..`/`//`, globs and braces, symlinks) and
     compared case-insensitively; a longer name such as `telegram-agent-cli-notes.md` is not the dir.
     A `telegram-agent-cli/` segment followed by a data-dir file name (`config.json`, `session.json`,
     `peers.json`, `pending-login.json`, `watchers.json`, `answered.jsonl`, `PAUSE-ALL`) counts
     whatever comes before it;
   - reads a directory tree that contains a data dir: `grep -r`, `rg` (with `--hidden`, or a path
     inside `.config`), `find … -exec`/`-delete` or piped to `xargs`, `tar`, `zip`, `cp -r`, `rsync`
     and similar on `~`, `~/.config` or `/`;
   - changes Claude Code settings: `settings.json`/`settings.local.json` in any `.claude` dir or
     `$CLAUDE_CONFIG_DIR`, and `managed-settings.json`. File tools ask when the text they write or
     replace names `hooks`, `PreToolUse`, `disableAllHooks`, a `TGAC_` variable or the binary, or when
     the `hooks`/`env` part of the file would change. Bash asks only when a settings file is the
     target of a write: a redirect, `tee`, `mv`/`rm` (also of a `.claude` dir), the destination of
     `cp`/`ln`/`rsync`, `sed -i`/`perl -i`, `dd of=`, `git checkout`/`restore`. Reading or
     mentioning it passes, and code asks only when it names a settings file and also writes a file;
   - mixes an MTProto library (Telethon, gotd, Pyrogram…) with a session file;
   - uses Read on `config.json`, `session.json`, `peers.json` or `pending-login.json` in the data dir,
     Grep on the data dir or on a directory that contains it (`~`, `~/.config`, `/`, or no path while
     the cwd is one of those), or Edit/Write/MultiEdit/NotebookEdit anywhere under it;
   - writes code (any string field of Write/Edit/MultiEdit/NotebookEdit, not the replaced text) that
     overrides `TGAC_READONLY`, sets the watcher variable, touches the data dir, writes Claude Code
     settings, or pairs an MTProto library with a session. Documentation files (`.md`, `.txt`,
     `.rst`…) are not scanned, so `go test` or `make` can't run unchecked code that was written
     silently.

   The name inside an argument does not count: `git commit -m "…telegram-agent-cli send…"`,
   `grep telegram-agent-cli README.md` or a `claude -p "…"` prompt pass silently. Values of
   `-m`/`--message`/`--body`/`--title` and grep/rg/`git grep` patterns are treated as text, unless
   they contain an expansion such as `$(…)`.

   How Claude Code handles "ask" (verified 2026-10-28 on Claude Code 2.1.307): interactive sessions
   show the permission prompt in every mode, including `bypassPermissions`; in headless `claude -p`
   runs, where nobody can answer, the call is denied.
4. **Watchers** have their own gate (registry, scopes, rate limits, pause files); see [watchers.md](watchers.md).

**Limits.** The hook is a second check, not a sandbox:

- Code running as your OS user can read the session file. The hook only sees the tool call, not
  what a program does once it runs.
- Interpreter code is followed through string literals, not executed: code that computes the
  command, builds a variable name in a way the hook can't read, or decodes its own payload can get
  past it. An agent can also write its own MTProto client.
- Programs that run other files on their own (`make`, `go test`, `npm run`) are not followed; their
  files are checked when an agent writes them with the file tools or a heredoc, not when they run.
- Content the hook can't see (a download piped into a shell, `$(…)` run as code, an unreadable
  script) is only flagged when it is decoded on the way in or the visible command links to the
  account. Obfuscated content with no visible link to the tool isn't caught; the binary's own write
  gate (`TGAC_READONLY`) is the main barrier.
- The prompt is a question to you, not your approval. In `bypassPermissions` mode it is the only
  thing between the agent and the command, so read each one before approving.

The session file is the real boundary: it is full access to the account. If you run agents you
don't trust, run them as a different OS user or in a container that cannot read the data dir.

## Claude Code hook details

`hook install` edits `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR/settings.json`, or
`--settings PATH`). It sets `env.TGAC_READONLY` to `"1"` and adds two `PreToolUse` entries, matcher
`Bash|Monitor` and matcher `Read|Edit|Write|MultiEdit|NotebookEdit|Grep`, each running
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
