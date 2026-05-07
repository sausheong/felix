---
name: wacli
description: Send WhatsApp messages, search local message history, manage contacts and groups, and sync WhatsApp data using the wacli CLI.
tags: [wacli, whatsapp, messaging, send, search, contacts, groups, sync, chat]
---

# wacli

## Purpose

Use `wacli` to interact with WhatsApp from the terminal. It pairs as a WhatsApp Web linked device, stores message history locally in SQLite, and exposes send, search, contact, group, and media workflows for scripts and automation.

## When to use this skill

Use `wacli` when you need to:

- Send a text message, file, or reaction to a WhatsApp contact or group
- Search or list stored WhatsApp message history offline
- Show a specific message or its surrounding context
- Look up contacts or chats by name or phone number
- Manage group membership, rename groups, or get invite links
- Download media from stored messages
- Check authentication status or run diagnostics
- Sync the local message store from WhatsApp

Do not use `wacli` when:

- You need a full GUI WhatsApp client
- The task involves WhatsApp Business API (wacli uses the personal Web protocol)
- You need guaranteed delivery confirmation — send is best-effort
- The store has not been authenticated yet (`wacli auth` must have been run first)

## Environment notes

`wacli` is a single binary. The store directory defaults to `~/.local/state/wacli` on Linux and `~/.wacli` on macOS/others. Override with `--store DIR` or `WACLI_STORE_DIR`.

### Check if installed

```bash
command -v wacli && wacli version
```

### Install if missing

- **macOS (Homebrew tap)**: `brew install steipete/tap/wacli`
- **From source (requires cgo + C compiler)**:
  ```bash
  CGO_ENABLED=1 go build -tags sqlite_fts5 -o wacli github.com/steipete/wacli/cmd/wacli
  ```

After installing, verify with `wacli version`. If installation fails, ask the user how they'd like to proceed.

### First-time setup

Authentication is required before any other command:

```bash
wacli auth        # Shows QR code — scan with WhatsApp on your phone
wacli sync --once # Bootstrap the local message store
```

To keep history current, run `wacli sync --follow` as a background process.

## Core principles

1. Always check `wacli doctor` before diagnosing send or sync failures.
2. Confirm recipient and message text before sending — sends cannot be recalled.
3. `wacli messages` commands work offline; `wacli send` requires a live connection.
4. Use `--json` when parsing output in scripts; default output is human-readable tables.
5. Use `--pick N` in non-interactive scripts when a name could match multiple recipients.
6. Avoid tight send loops — rapid sends (< 5 s apart) warn on stderr and risk rate-limiting.
7. JIDs are the authoritative recipient identifier; resolve names with `wacli contacts search` first.

## Global flags

| Flag | Effect |
|------|--------|
| `--store DIR` | Override the store directory |
| `--json` | Machine-readable JSON output |
| `--full` | Disable table column truncation |
| `--read-only` | Reject commands that write state |
| `--lock-wait DURATION` | Wait for the store lock instead of failing |

`WACLI_STORE_DIR` and `WACLI_READONLY=1` are the environment equivalents.

## Command reference

### Auth

```bash
wacli auth              # Pair device via QR code and bootstrap sync
wacli auth status       # Show linked-device status
wacli auth logout       # Unlink this device
```

### Sync

```bash
wacli sync --once                  # Sync messages, contacts, groups once then exit
wacli sync --follow                # Continuous background sync
wacli sync --media                 # Also download media during sync
wacli sync --max-db-size SIZE      # Bound store growth (e.g. 500MB)
```

### Messages

`wacli messages` reads from the local store — no live connection required.

```bash
# List messages
wacli messages list [--chat JID] [--sender JID] [--from-me|--from-them] \
                    [--asc] [--limit N] [--after DATE] [--before DATE] [--forwarded]

# Search (SQLite FTS5 when available; LIKE fallback)
wacli messages search <query> [--chat JID] [--from JID] [--has-media] \
                               [--type text|image|video|audio|document] \
                               [--limit N] [--after DATE] [--before DATE]

# Show a single message
wacli messages show --chat JID --id MSG_ID

# Show surrounding context
wacli messages context --chat JID --id MSG_ID [--before N] [--after N]
```

Time filters accept RFC3339 or `YYYY-MM-DD`.

### Send

Requires auth and a live connection.

```bash
# Text message
wacli send text --to RECIPIENT --message TEXT \
                [--pick N] [--reply-to MSG_ID] [--reply-to-sender JID]

# File upload (max 100 MiB; MIME auto-detected)
wacli send file --to RECIPIENT --file PATH \
                [--pick N] [--caption TEXT] [--filename NAME] [--mime TYPE] \
                [--reply-to MSG_ID]

# Reaction (defaults to 👍; pass --reaction "" to clear)
wacli send react --to PHONE_OR_JID --id MSG_ID [--reaction TEXT] [--sender JID]
```

**Recipient formats**: JID (`1234567890@s.whatsapp.net`), group JID (`123@g.us`), phone number (`+1 234 567 8900`), or a synced contact/group/chat name. Ambiguous names prompt interactively; use `--pick N` in scripts.

### Contacts

```bash
wacli contacts search <query>        # Search by name or number
wacli contacts show PHONE_OR_JID     # Show contact details
wacli contacts refresh               # Pull latest contacts from WhatsApp
```

### Chats

```bash
wacli chats list                     # List known chats
wacli chats show JID                 # Show chat details
```

### Groups

```bash
wacli groups list                    # List groups
wacli groups refresh                 # Refresh group metadata
wacli groups info JID                # Group details and participants
wacli groups rename JID --name NAME  # Rename (admin only)
wacli groups leave JID               # Leave a group
wacli groups participants JID        # List participants
wacli groups invite JID              # Get invite link
wacli groups join --link URL         # Join via invite link
```

### History backfill

Ask the primary phone for older per-chat history (best-effort; phone must be online):

```bash
wacli history backfill --chat JID [--requests N] [--count N]
```

### Media

```bash
wacli media download --chat JID --id MSG_ID   # Download media from a stored message
```

### Presence

```bash
wacli presence typing --to JID       # Send typing indicator
wacli presence paused --to JID       # Send paused indicator
```

### Profile

```bash
wacli profile set-picture --file PATH  # Set account profile picture (JPEG or PNG)
```

### Diagnostics

```bash
wacli doctor               # Check store health, auth, and FTS5 search
wacli doctor --connect     # Also test live WhatsApp connectivity
```

## JID reference

| Type | Format |
|------|--------|
| Individual | `1234567890@s.whatsapp.net` |
| Group | `1234567890-123456789@g.us` |

Use `wacli contacts search <name>` or `wacli chats list` to look up JIDs.

## Examples

### Send a text message

```bash
wacli send text --to mom --message "landed safely"
wacli send text --to "Family Group" --pick 1 --message "on my way"
wacli send text --to +14155551212 --message "see attached" --reply-to ABC123
```

### Send a file

```bash
wacli send file --to +14155551212 --file ./photo.jpg --caption "here you go"
wacli send file --to +14155551212 --file /tmp/report.pdf --filename report.pdf
```

### Search messages

```bash
wacli messages search "invoice" --has-media --type document
wacli messages search "meeting" --after 2025-01-01 --before 2025-06-01
```

### List recent messages from a chat

```bash
wacli messages list --chat 1234567890@s.whatsapp.net --asc --limit 50
```

### Look up a contact's JID

```bash
wacli contacts search "Alice"
```

### Check store and connectivity

```bash
wacli doctor --connect
```

## Workflow

1. Verify `wacli` is installed and authenticated (`wacli doctor`).
2. If searching history, use `wacli messages search` or `wacli messages list` — no connection needed.
3. If sending, resolve the recipient JID first with `wacli contacts search` or use a phone number directly.
4. Confirm recipient and message text before calling `wacli send`.
5. For scripts, pass `--json` and `--pick N` to avoid interactive prompts.

## Troubleshooting

- **"not authenticated"**: Run `wacli auth` and scan the QR code with your phone.
- **Send fails with timeout/usync error**: `wacli` retries once after reconnect automatically. If it still fails, run `wacli doctor --connect` to check connectivity.
- **Search returns no results**: Check that `wacli sync --once` has been run and the store has data. FTS5 may not be available in the installed binary; LIKE fallback is used automatically.
- **Ambiguous recipient error in script**: Use a JID directly or add `--pick N`.
- **Store lock conflict**: Another `wacli` process is running. Use `--lock-wait 10s` to wait, or stop the other process.
- **Media not found**: Run `wacli media download` to fetch it on demand, or use `wacli sync --media` to bulk-download during sync.
