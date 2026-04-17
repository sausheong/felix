package google

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sausheong/felix/internal/tools"
	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// GmailListRecentTool returns the N most recent inbox messages with sender,
// subject, date, and snippet. This is the proof-of-life tool for the Google
// integration: if it works, OAuth + token refresh + API client wiring all work.
type GmailListRecentTool struct {
	Manager *Manager
}

type gmailListInput struct {
	Limit int    `json:"limit"`
	Query string `json:"query"`
}

func (t *GmailListRecentTool) Name() string { return "gmail_list_recent" }

func (t *GmailListRecentTool) Description() string {
	return `List recent Gmail messages from the connected Google account. Returns sender, subject, date, and snippet for each. Use this to check what's in the inbox or search for specific messages. Optional 'query' uses Gmail search syntax (e.g. "from:alice", "is:unread", "subject:invoice").`
}

func (t *GmailListRecentTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"limit": {"type": "integer", "description": "How many messages to return (1-50). Default 10.", "minimum": 1, "maximum": 50},
			"query": {"type": "string", "description": "Optional Gmail search query (e.g. 'is:unread from:alice@example.com')"}
		}
	}`)
}

func (t *GmailListRecentTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var in gmailListInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return tools.ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
		}
	}
	if in.Limit <= 0 {
		in.Limit = 10
	}
	if in.Limit > 50 {
		in.Limit = 50
	}

	if t.Manager == nil || !t.Manager.IsConnected() {
		return tools.ToolResult{Error: "google account not connected (visit /settings to set up)"}, nil
	}

	httpClient, err := t.Manager.HTTPClient(ctx)
	if err != nil {
		return tools.ToolResult{Error: fmt.Sprintf("auth failed: %v", err)}, nil
	}

	svc, err := gmail.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return tools.ToolResult{Error: fmt.Sprintf("gmail client: %v", err)}, nil
	}

	listCall := svc.Users.Messages.List("me").MaxResults(int64(in.Limit))
	if in.Query != "" {
		listCall = listCall.Q(in.Query)
	}
	resp, err := listCall.Context(ctx).Do()
	if err != nil {
		return tools.ToolResult{Error: fmt.Sprintf("gmail list failed: %v", err)}, nil
	}

	var lines []string
	for _, m := range resp.Messages {
		// Fetch metadata-only (no body) for each — keeps the response small
		full, err := svc.Users.Messages.Get("me", m.Id).
			Format("metadata").
			MetadataHeaders("From", "Subject", "Date").
			Context(ctx).Do()
		if err != nil {
			lines = append(lines, fmt.Sprintf("[%s] (failed to fetch: %v)", m.Id, err))
			continue
		}
		from, subject, date := headerLookup(full)
		snippet := strings.TrimSpace(full.Snippet)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		lines = append(lines,
			fmt.Sprintf("[%s] %s\n  From: %s\n  Date: %s\n  Subject: %s\n  %s",
				m.Id, displayLabels(full.LabelIds), from, date, subject, snippet))
	}

	if len(lines) == 0 {
		return tools.ToolResult{Output: "No messages matched."}, nil
	}
	return tools.ToolResult{Output: strings.Join(lines, "\n\n")}, nil
}

func headerLookup(m *gmail.Message) (from, subject, date string) {
	if m == nil || m.Payload == nil {
		return
	}
	for _, h := range m.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "from":
			from = h.Value
		case "subject":
			subject = h.Value
		case "date":
			date = h.Value
		}
	}
	return
}

func displayLabels(ids []string) string {
	// Show only meaningful flags so the line stays scannable
	keep := map[string]string{
		"UNREAD":    "unread",
		"IMPORTANT": "important",
		"STARRED":   "starred",
	}
	var out []string
	for _, id := range ids {
		if v, ok := keep[id]; ok {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "(" + strings.Join(out, ",") + ")"
}
