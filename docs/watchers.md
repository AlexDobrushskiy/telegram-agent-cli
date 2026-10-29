# Watchers

A watcher is a long-running agent session allowed to answer certain incoming messages on its own,
for example a support helper that replies to people who write first. It is the designated path for replies without
per-message approval, and nothing is allowed until you add an entry to `watchers.json` in the data dir yourself
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
the calling session must present (env `TGAC_WATCHER_SESSION`, configurable), `chats` lists
allowed chats (chat id → note): it is the whole allowlist for `listed` scope and for groups, while
`contacts` also admits mutual contacts and `dm-first` admits people who wrote first. The optional `dir` is the watcher's own workdir; a `PAUSE` file there
pauses it, and the watcher may keep notes there, but nothing in it grants anything. Every attempt that reaches the send is logged to `<data dir>/watchers/NAME/sends.jsonl` (refusals
and dry runs are not). The log lives in the data dir, not the watcher's workdir, so the hook guards
it (this is not OS-level isolation); `<data dir>/watchers/NAME/PAUSE` also pauses it. `watch reply` works in read-only
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
changing `session`, delisting a chat that only the list allowed, or adding a `PAUSE` stops a reply
that hasn't been sent yet.
`--dry-run` runs every check and reports what it would send. `watch list` and `watch check --watcher
NAME` validate the registry offline and show only whether a session binding is set. Stop
everything at once with `touch <data dir>/PAUSE-ALL`.

## Running one

Registering a watcher doesn't start anything: you run the agent yourself, with the session id from
its entry in its environment. For example:

```sh
export TGAC_WATCHER_SESSION=the-watcher-session-id   # must equal "session" in watchers.json
# then start your agent with a brief like: loop on
#   telegram-agent-cli messages wait-new --state state/wm.json --dms
# draft a reply for each new message, test it with
#   telegram-agent-cli watch reply --watcher helper --chat ID --text "…" --dry-run
# and send it with the same command without --dry-run.
```

Set the variable when you launch the agent, not from inside it: the hook asks before an agent sets
it itself.
