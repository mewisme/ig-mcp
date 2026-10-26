package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"go.mewis.me/ig-mcp/internal/envloader"
)

func newDoctorCommand(envPath *string) *cobra.Command {
	var mode string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check local dotenv, Instagram session and transport configuration without network access",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mode != "openai" && mode != "stdio" && mode != "cloudflare" && mode != "cf" {
				return fmt.Errorf("unsupported transport %q (use openai, cloudflare, cf or stdio)", mode)
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			report := doctorReport(cwd, *envPath, mode)
			_, err = fmt.Fprint(cmd.OutOrStdout(), report)
			return err
		},
	}
	cmd.Flags().StringVar(&mode, "transport", "openai", "Transport to check: openai, cloudflare (cf) or stdio")
	return cmd
}

func doctorReport(cwd, envPath, mode string) string {
	var out strings.Builder
	if err := envloader.Load(cwd, envPath); err != nil {
		out.WriteString("WARN dotenv: cannot load configuration (check file syntax, location and permissions)\n")
	} else if hasLocalDotenv(cwd, envPath) {
		out.WriteString("PASS dotenv: local configuration loaded (inherited environment takes precedence)\n")
	} else {
		out.WriteString("WARN dotenv: no local dotenv file found (inherited environment may still be used)\n")
	}

	state := localSessionState()
	if state == "session_saved" || state == "ready" {
		fmt.Fprintf(&out, "PASS session: %s (local check only; not verified online)\n", state)
	} else {
		fmt.Fprintf(&out, "WARN session: %s (%s)\n", state, sessionStatusCause(state))
	}

	if mode == "stdio" {
		out.WriteString("PASS transport: stdio does not require OpenAI tunnel credentials\n")
	} else if mode == "cloudflare" || mode == "cf" {
		secret := os.Getenv("IG_MCP_CF_BEARER_TOKEN")
		if os.Getenv("CF_TUNNEL_TOKEN") != "" {
			out.WriteString("WARN cloudflare: CF_TUNNEL_TOKEN is not supported for in-memory Quick Tunnel; unset it\n")
		} else if !validDoctorCloudflareToken(secret) {
			out.WriteString("WARN cloudflare: IG_MCP_CF_BEARER_TOKEN must contain 32+ characters of random printable ASCII to protect the public MCP endpoint\n")
		} else {
			out.WriteString("PASS cloudflare: bearer token configured (internet connection not tested)\n")
		}
	} else {
		var missing []string
		for _, key := range []string{"CONTROL_PLANE_TUNNEL_ID", "CONTROL_PLANE_API_KEY"} {
			if strings.TrimSpace(os.Getenv(key)) == "" {
				missing = append(missing, key)
			}
		}
		if len(missing) == 0 {
			out.WriteString("PASS tunnel: required environment variables are present (not tested online)\n")
		} else {
			fmt.Fprintf(&out, "WARN tunnel: missing %s\n", strings.Join(missing, ", "))
		}
	}
	return out.String()
}

func validDoctorCloudflareToken(token string) bool {
	if len(token) < 32 {
		return false
	}
	for i := 0; i < len(token); i++ {
		if token[i] < '!' || token[i] > '~' {
			return false
		}
	}
	return true
}

func hasLocalDotenv(cwd, envPath string) bool {
	paths := []string{filepath.Join(cwd, ".env"), filepath.Join(cwd, ".env.local")}
	if envPath != "" {
		if !filepath.IsAbs(envPath) {
			envPath = filepath.Join(cwd, envPath)
		}
		paths = append(paths, envPath)
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}
	return false
}
