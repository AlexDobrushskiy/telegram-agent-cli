# Records docs/demo.gif. Run from the repo root: vhs demo/demo.tape
# The demo build (-tags demo) runs against a fictional in-memory account; it never connects to Telegram.

Output docs/demo.gif

Require go
Require jq

Set Shell bash
Set FontSize 16
Set Width 1360
Set Height 460
Set Padding 22
Set Theme "Catppuccin Mocha"
Set TypingSpeed 11ms
Set Framerate 20

Hide
Type "go build -tags demo -o /tmp/tgac-demo ./cmd/telegram-agent-cli && echo built" Enter
Wait+Screen@90s /built/
Type 'W=$(mktemp -d) && mkdir -m 700 "$W/home" && cp demo/watchers.json "$W/home/" && cp demo/send.json demo/read.json "$W/" && cd "$W"' Enter
Type 'export TGAC_HOME="$W/home" TGAC_WATCHER_SESSION=demo-session-7f3a && set -o pipefail' Enter
Type 'alias telegram-agent-cli=/tmp/tgac-demo' Enter
Type `export PS1='\[\e[1;35m\]$\[\e[0m\] '` Enter
Type "clear" Enter
Sleep 300ms
Show

Type "# Your own Telegram account as JSON, read-only by default" Enter
Type "telegram-agent-cli chats list --unread | jq -c '.data.chats[] | {name, unread, mentions}'" Sleep 150ms Enter
Sleep 2s
Type "telegram-agent-cli messages search invoice | jq -c '.data.messages[] | {chat: .chat_name, text}'" Sleep 150ms Enter
Sleep 2s
Type "telegram-agent-cli messages mentions --unread | jq -c '.data.messages[] | {from: .sender_name, text}'" Sleep 150ms Enter
Sleep 2.2s

Hide
Type "clear" Enter
Sleep 200ms
Show

Type "# Writes are refused unless TGAC_READONLY=0" Enter
Type `telegram-agent-cli send text --chat 'Priya Raman' --message 'See you Thursday' | jq -c .error; echo "exit $?"` Sleep 150ms Enter
Sleep 2.5s
Type "# Claude Code hook: a send asks you first, a read passes silently" Enter
Type "jq -r .tool_input.command send.json read.json" Sleep 150ms Enter
Sleep 1.5s
Type "telegram-agent-cli hook claude-pretooluse < send.json | jq -r .hookSpecificOutput.permissionDecision" Sleep 150ms Enter
Sleep 1.8s
Type `telegram-agent-cli hook claude-pretooluse < read.json; echo "(no output, exit $?)"` Sleep 150ms Enter
Sleep 2s
Type "# Watchers: gated autonomous replies; --dry-run runs every check" Enter
Type "telegram-agent-cli watch reply --watcher helper --chat 5550102 --text 'Yes, 10am!' --dry-run | jq -c .data.would_send" Sleep 150ms Enter
Sleep 3.5s

Hide
Type 'cd / && rm -rf "$W"' Enter
