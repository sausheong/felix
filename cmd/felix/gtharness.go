package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sausheong/felix/internal/mcp"
)

func gtHarnessCmd() *cobra.Command {
	var (
		envFile  string
		callTool string
		callArgs string
	)
	cmd := &cobra.Command{
		Use:   "gt-harness",
		Short: "Smoke-test connection to a remote MCP gateway (experimental)",
		Long: "Reads OAuth client-credentials from a dotenv-style file, opens an " +
			"MCP Streamable-HTTP session, and lists (or invokes) tools.\n\n" +
			"Required env keys: MCP_SERVER_URL, LTM_CLIENT_ID, LTM_CLIENT_SECRET, " +
			"LTM_TOKEN_URL, LTM_SCOPE.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGtHarness(cmd.Context(), os.Stdout, envFile, callTool, callArgs)
		},
	}
	cmd.Flags().StringVar(&envFile, "env-file", "gt-harness.txt", "path to KEY=VALUE credentials file")
	cmd.Flags().StringVar(&callTool, "call", "", "optional: name of a tool to invoke after listing")
	cmd.Flags().StringVar(&callArgs, "args", "{}", "JSON object of arguments for --call")
	return cmd
}

func runGtHarness(ctx context.Context, out io.Writer, envFile, callTool, callArgs string) error {
	env, err := mcp.LoadEnvFile(envFile)
	if err != nil {
		return err
	}
	if err := mcp.RequireKeys(env,
		"MCP_SERVER_URL", "LTM_CLIENT_ID", "LTM_CLIENT_SECRET",
		"LTM_TOKEN_URL", "LTM_SCOPE",
	); err != nil {
		return err
	}

	httpClient := mcp.NewClientCredentialsHTTPClient(mcp.ClientCredentialsConfig{
		TokenURL:     env["LTM_TOKEN_URL"],
		ClientID:     env["LTM_CLIENT_ID"],
		ClientSecret: env["LTM_CLIENT_SECRET"],
		Scope:        env["LTM_SCOPE"],
	})

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client, err := mcp.ConnectHTTP(connectCtx, env["MCP_SERVER_URL"], httpClient)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer client.Close()

	tools, err := client.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}

	fmt.Fprintf(out, "Connected to %s\n", env["MCP_SERVER_URL"])
	fmt.Fprintf(out, "Tools (%d):\n", len(tools))
	for _, t := range tools {
		fmt.Fprintf(out, "  - %s\n      %s\n", t.Name, t.Description)
		if len(t.InputSchema) > 0 {
			fmt.Fprintf(out, "      schema: %s\n", string(t.InputSchema))
		}
	}

	if callTool == "" {
		return nil
	}

	var argsMap map[string]any
	if err := json.Unmarshal([]byte(callArgs), &argsMap); err != nil {
		return fmt.Errorf("--args must be a JSON object: %w", err)
	}

	fmt.Fprintf(out, "\nCalling %s(%s) ...\n", callTool, callArgs)
	res, err := client.CallTool(ctx, callTool, argsMap)
	if err != nil {
		return err
	}
	if res.IsError {
		fmt.Fprintf(out, "Tool returned error.\n")
	}
	if res.Text != "" {
		fmt.Fprintf(out, "Text:\n%s\n", res.Text)
	}
	fmt.Fprintf(out, "Raw:\n%s\n", string(res.Raw))
	return nil
}
