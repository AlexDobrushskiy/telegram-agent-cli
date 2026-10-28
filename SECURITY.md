# Security

## What to protect

- **`session.json`** in the data dir (`$TGAC_HOME`, default `~/.config/telegram-agent-cli/`) is a
  logged-in Telegram device. Anyone with this file can read every chat, send as you, and change
  account settings, without your phone or password. Treat it like a private key: keep the data dir
  at mode 0700 and its files at 0600 (`doctor` reports drift), never commit, sync, back up to shared
  storage, paste or upload it.
- **`config.json`** holds your `api_hash`. It doesn't grant account access, but keep it private.
- If you suspect a session leaked, terminate it in Telegram: **Settings → Devices** → the
  `telegram-agent-cli` session → Terminate. That revokes the key on Telegram's side immediately.

The read-only middleware, the default `TGAC_READONLY`, and the Claude Code hook reduce what an agent
can do by accident or by following injected instructions. They are not a sandbox: code running as
your OS user can read the session file. Run untrusted agents as a separate user or in a container
without access to the data dir. See "Safety model" in the [README](README.md).

## Reporting a vulnerability

Please report privately through GitHub: the repository's **Security** tab → **Report a
vulnerability**. Don't open a public issue for anything that could let a write slip past the
read-only guard or the hook, expose session material, or bypass a watcher rule. Include the version
(`telegram-agent-cli version`), the command or input, and what happened.

You should get a reply within a week. Fixes go into the latest release; older versions are not
patched.
