---
name: gog
description: Google Workspace CLI for email, calendar, and file management via Gmail, Google Calendar, and Google Drive.
tags: [email, emails, mail, gmail, calendar, events, meetings, schedule, drive, files, google, workspace, send, inbox, drafts]
homepage: https://gogcli.sh
metadata:
  {
    "manyhands":
      {
        "requires": { "bins": ["gog"] },
        "install":
          [
            {
              "id": "brew",
              "kind": "brew",
              "formula": "steipete/tap/gogcli",
              "bins": ["gog"],
              "label": "Install gog (brew)",
            },
          ],
      },
  }
---

# gog

Use `gog` for Gmail/Calendar/Drive.

Authentication is automatic in ManyHands — the user connects Google Workspace
in the hand's Settings page, and `GOG_ACCESS_TOKEN` is injected into the
environment when the hand starts. No manual OAuth setup is needed.
If `gog` reports authentication errors, ask the user to reconnect Google
Workspace in Settings and restart the hand.

## Install if missing

### Check if installed

```bash
command -v gog && gog --version
```

### Install

If `gog` is not found, install before running any other command in this skill:

- **macOS / Linux (Homebrew)**: `brew install steipete/tap/gogcli`
- **From source (any platform with Go)**: `go install github.com/steipete/gogcli/cmd/gog@latest`
- **Manual**: download a release binary from the gog homepage (https://gogcli.sh) and place it on `PATH`.

After installing, re-check with `gog --version` before continuing. If installation fails, ask the user how they'd like to proceed.

Common commands

- Gmail search: `gog gmail search 'newer_than:7d' --max 10`
- Gmail messages search (per email, ignores threading): `gog gmail messages search "in:inbox from:ryanair.com" --max 20`
- Gmail send (plain): `gog gmail send --to a@b.com --subject "Hi" --body "Hello"`
- Gmail send (multi-line): `gog gmail send --to a@b.com --subject "Hi" --body-file ./message.txt`
- Gmail send (stdin): `gog gmail send --to a@b.com --subject "Hi" --body-file -`
- Gmail send (HTML): `gog gmail send --to a@b.com --subject "Hi" --body-html "<p>Hello</p>"`
- Gmail draft: `gog gmail drafts create --to a@b.com --subject "Hi" --body-file ./message.txt`
- Gmail send draft: `gog gmail drafts send <draftId>`
- Gmail reply: `gog gmail send --to a@b.com --subject "Re: Hi" --body "Reply" --reply-to-message-id <msgId>`
- Calendar list events: `gog calendar events <calendarId> --from <iso> --to <iso>`
- Calendar create event: `gog calendar create <calendarId> --summary "Title" --from <iso> --to <iso>`
- Calendar create with color: `gog calendar create <calendarId> --summary "Title" --from <iso> --to <iso> --event-color 7`
- Calendar update event: `gog calendar update <calendarId> <eventId> --summary "New Title" --event-color 4`
- Calendar show colors: `gog calendar colors`
- Drive list files: `gog drive ls [folderId] --max 20`
- Drive search: `gog drive search "query" --max 10`
- Drive get metadata: `gog drive get <fileId>`
- Drive download: `gog drive download <fileId> --out ./filename`
- Drive upload: `gog drive upload ./localfile --parent <folderId>`
- Drive mkdir: `gog drive mkdir "Folder Name" --parent <folderId>`
- Drive delete (trash): `gog drive delete <fileId>`
- Drive delete (permanent): `gog drive delete <fileId> --permanent`
- Drive move: `gog drive move <fileId> --to <folderId>`
- Drive rename: `gog drive rename <fileId> "New Name"`
- Drive copy: `gog drive copy <fileId> "Copy Name"`
- Drive share: `gog drive share <fileId> --email user@example.com --role writer`
- Drive get URL: `gog drive url <fileId>`

Notes

- Authentication is handled automatically via `GOG_ACCESS_TOKEN` env var (set by ManyHands on hand start).
- Access tokens expire after ~1 hour. If `gog` gets auth errors, the user should restart the hand to get a fresh token.
- Prefer plain text for email. Use `--body-file` for multi-line messages (or `--body-file -` for stdin).
- `--body` does not unescape `\n`. Use a heredoc or `$'Line 1\n\nLine 2'` for inline newlines.
- Use `--body-html` only when rich formatting is needed.
- Confirm before sending mail or creating events.
- `gog gmail search` returns one row per thread; use `gog gmail messages search` for individual emails.
