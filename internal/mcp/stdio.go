package mcp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ConnectStdio spawns a subprocess and opens an MCP session against it over
// stdin/stdout (newline-delimited JSON-RPC). Stderr is captured and forwarded
// to slog at Debug level under "mcp stdio stderr" so misbehaving servers
// surface in verbose logs without spamming default output.
//
// id is used purely for log labelling. command is looked up via PATH (so
// "npx", "python", etc. work without an absolute path). env is merged onto
// os.Environ() — later entries win, so PATH and other inherited vars stay
// present unless explicitly overridden.
//
// On any failure (binary missing, MCP handshake refused, etc.) the spawned
// process is cleaned up by the SDK transport's failure path and an error is
// returned. The Manager's existing log+skip behavior treats this identically
// to an unreachable HTTP server.
func ConnectStdio(ctx context.Context, id, command string, args []string, env map[string]string) (*Client, error) {
	if command == "" {
		return nil, fmt.Errorf("mcp stdio %s: empty command", id)
	}
	cmd := exec.Command(command, args...)
	cmd.Env = mergedEnv(os.Environ(), env)

	// StderrPipe must be wired up before CommandTransport.Connect calls
	// cmd.Start — calling StderrPipe after Start panics. The forwarder
	// goroutine exits naturally when the process closes stderr (EOF).
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio %s: stderr pipe: %w", id, err)
	}
	go forwardStderr(stderr, id)

	transport := &mcpsdk.CommandTransport{Command: cmd}
	sdkClient := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "felix",
		Version: "0.0.0-stdio",
	}, nil)

	session, err := sdkClient.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio connect %s (%s): %w", id, command, err)
	}
	return &Client{session: session}, nil
}

// mergedEnv returns parent ++ overrides, deduplicated by KEY with later
// entries winning. Preserves the parent slice's order for keys that aren't
// overridden. New keys from overrides are appended at the end in iteration
// order (which is non-deterministic for maps, but env-var order has no
// semantics in POSIX, so this is fine).
func mergedEnv(parent []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return parent
	}
	idx := make(map[string]int, len(parent))
	out := make([]string, 0, len(parent)+len(overrides))
	for _, kv := range parent {
		key, _, _ := strings.Cut(kv, "=")
		idx[key] = len(out)
		out = append(out, kv)
	}
	for k, v := range overrides {
		entry := k + "=" + v
		if i, ok := idx[k]; ok {
			out[i] = entry
		} else {
			idx[k] = len(out)
			out = append(out, entry)
		}
	}
	return out
}

func forwardStderr(r io.ReadCloser, id string) {
	defer r.Close()
	scanner := bufio.NewScanner(r)
	// Bump the max line size — some MCP servers print noisy stack traces.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		slog.Debug("mcp stdio stderr", "id", id, "line", scanner.Text())
	}
	// scanner.Err() at this point is normal process exit / pipe close.
	// We deliberately do not log it — every stdio server will trip this on
	// shutdown and warning-level noise would mask real issues.
}
