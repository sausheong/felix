package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/local"
)

func modelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Manage models served by the bundled Ollama supervisor",
	}
	cmd.AddCommand(
		modelListCmd(),
		modelPullCmd(),
		modelRemoveCmd(),
		modelStatusCmd(),
	)
	return cmd
}

func defaultLocalBaseURL() string {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return "http://127.0.0.1:18790"
	}
	pc, ok := cfg.Providers["local"]
	if !ok || pc.BaseURL == "" {
		return "http://127.0.0.1:18790"
	}
	// Strip the trailing /v1 — the Ollama API uses bare endpoints.
	url := pc.BaseURL
	if len(url) > 3 && url[len(url)-3:] == "/v1" {
		url = url[:len(url)-3]
	}
	return url
}

func modelListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List models pulled into the bundled Ollama",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelList(cmd.Context(), defaultLocalBaseURL(), os.Stdout)
		},
	}
}

func runModelList(ctx context.Context, baseURL string, out io.Writer) error {
	inst := local.NewInstaller(baseURL)
	models, err := inst.List(ctx)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		fmt.Fprintln(out, "No models pulled. Try: felix model pull qwen2.5:0.5b")
		return nil
	}
	for _, m := range models {
		fmt.Fprintf(out, "%-30s %s\n", m.Name, humanizeBytes(m.SizeBytes))
	}
	return nil
}

func modelPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <name>",
		Short: "Pull a model into the bundled Ollama",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelPull(cmd.Context(), defaultLocalBaseURL(), args[0], os.Stdout)
		},
	}
}

func runModelPull(ctx context.Context, baseURL, name string, out io.Writer) error {
	inst := local.NewInstaller(baseURL)
	var lastDigest string
	return inst.Pull(ctx, name, func(ev local.ProgressEvent) {
		if ev.Total > 0 && ev.Digest != lastDigest {
			fmt.Fprintf(out, "\n%s %s\n", ev.Status, ev.Digest)
			lastDigest = ev.Digest
		}
		if ev.Total > 0 {
			pct := float64(ev.Completed) / float64(ev.Total) * 100
			fmt.Fprintf(out, "\r  %.1f%% (%s / %s)", pct,
				humanizeBytes(ev.Completed), humanizeBytes(ev.Total))
		} else if ev.Status != "" {
			fmt.Fprintf(out, "\n%s\n", ev.Status)
		}
	})
}

func modelRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove a model from the bundled Ollama",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelRemove(cmd.Context(), defaultLocalBaseURL(), args[0], os.Stdout)
		},
	}
}

func runModelRemove(ctx context.Context, baseURL, name string, out io.Writer) error {
	inst := local.NewInstaller(baseURL)
	if err := inst.Delete(ctx, name); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed %s\n", name)
	return nil
}

func modelStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show bundled-Ollama status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelStatus(cmd.Context(), defaultLocalBaseURL(), os.Stdout)
		},
	}
}

func runModelStatus(ctx context.Context, baseURL string, out io.Writer) error {
	fmt.Fprintf(out, "base_url:  %s\n", baseURL)
	fmt.Fprintf(out, "models_dir: %s\n", local.DefaultModelsDir())

	inst := local.NewInstaller(baseURL)
	if _, err := inst.List(ctx); err != nil {
		fmt.Fprintf(out, "status:    unreachable (%v)\n", err)
		return nil
	}
	fmt.Fprintln(out, "status:    ready")
	return nil
}

func humanizeBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
