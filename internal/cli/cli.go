// Package cli defines the user-facing Instagram MCP command-line application.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"go.mewis.me/ig-mcp/internal/envloader"
	"go.mewis.me/ig-mcp/internal/instagram"
	"go.mewis.me/ig-mcp/internal/mcptools"
	"go.mewis.me/ig-mcp/internal/transport"
)

// NewRootCommand builds a fresh Cobra command tree so it can be tested without
// reusing mutable flag state. No session or .env file is loaded until execution.
func NewRootCommand(envExample string) *cobra.Command {
	var envPath string
	root := &cobra.Command{
		Use:           "ig-mcp",
		Short:         "Instagram-only MCP server",
		Long:          "Instagram Direct Messages over MCP. Authenticate interactively using the CLI; MCP tools never expose login or logout.",
		Version:       versionText(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Name() == "doctor" || cmd.Name() == "version" || (cmd.Name() == "env" && cmd.Parent() != nil && cmd.Parent().Name() == "init") {
				return nil
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return envloader.Load(cwd, envPath)
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.PersistentFlags().StringVar(&envPath, "env-path", "", "Additional dotenv file to load after .env and .env.local (inherited environment wins)")
	root.AddCommand(newInitCommand(envExample), newVersionCommand(), newDoctorCommand(&envPath), newLoginCommand(), newLogoutCommand(), newStatusCommand(), newServeCommand())
	return root
}

func newLoginCommand() *cobra.Command {
	var method string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to Instagram interactively; credentials are never accepted as flags",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch method {
			case "auto", "android", "web", "cookie":
			default:
				return fmt.Errorf("unsupported login method %q (use auto, cookie, android or web)", method)
			}
			svc := instagram.New()
			if err := svc.Login(cmd.Context(), method); err != nil {
				return err
			}
			account, verified := svc.VerifiedAccount()
			if !verified {
				return errors.Join(errors.New("Instagram login did not complete session validation"), svc.Close())
			}
			if err := svc.Close(); err != nil {
				return fmt.Errorf("finalize authenticated Instagram session: %w", err)
			}
			return writeLoginConfirmation(cmd.OutOrStdout(), account)
		},
	}
	cmd.Flags().StringVar(&method, "method", "auto", "Instagram authentication: auto (cookie if configured, otherwise android), cookie, android or web")
	return cmd
}

func writeLoginConfirmation(out io.Writer, account instagram.AccountInfo) error {
	if _, err := fmt.Fprintln(out, "Instagram login successful. Session verified and saved."); err != nil {
		return err
	}
	if account.Username != "" {
		if _, err := fmt.Fprintf(out, "Username: @%s\n", account.Username); err != nil {
			return err
		}
	}
	if account.FullName != "" {
		if _, err := fmt.Fprintf(out, "Name: %s\n", account.FullName); err != nil {
			return err
		}
	}
	if account.ID != "" {
		if _, err := fmt.Fprintf(out, "User ID: %s\n", account.ID); err != nil {
			return err
		}
	}
	return nil
}

func newStatusCommand() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether a local Instagram session exists, without contacting Instagram",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			state := localSessionState()
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), state); err != nil {
				return err
			}
			if verbose {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "Cause: "+sessionStatusCause(state))
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show safe local session diagnostics (never prints file paths or credentials)")
	return cmd
}

func localSessionState() string {
	state := instagram.New().Status()
	// A missing session is not an authentication failure. Check the local
	// filesystem independently when a session implementation reports it as
	// generic unavailable (for example, after a failed metadata/lock check).
	if state == "unavailable" {
		if path := sessionFilePath(); filepath.IsAbs(path) {
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				return "not_logged_in"
			}
		}
	}
	return state
}

func sessionFilePath() string {
	if path := strings.TrimSpace(os.Getenv("IG_MCP_SESSION_FILE")); path != "" {
		return path
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ig-mcp", "session.json")
}

func sessionStatusCause(state string) string {
	switch state {
	case "not_logged_in":
		return "no saved Instagram session; run 'ig-mcp login'"
	case "session_saved":
		return "saved session exists locally; launch 'ig-mcp serve' to verify it with Instagram"
	case "ready":
		return "session verified in this process; this is not a realtime connectivity check"
	case "expired":
		return "credentials expired or Instagram requires verification; log in again"
	case "connecting":
		return "session initialization is still in progress"
	case "closed":
		return "Instagram service is closed"
	case "unavailable":
		path := sessionFilePath()
		if path == "" || !filepath.IsAbs(path) {
			return "session path is unavailable or not absolute; check IG_MCP_SESSION_FILE"
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "local session file cannot be inspected; check its location and access permissions"
		}
		if !info.Mode().IsRegular() {
			return "local session path is not a regular file (symlinks are not accepted)"
		}
		if info.Mode().Perm()&0077 != 0 {
			return "local session file is not private; restrict access to the owning user (0600 on Unix)"
		}
		return "saved session is unreadable, malformed or missing required credentials; run 'ig-mcp login' to repair it"
	default:
		return "session state is not recognized; check the local configuration"
	}
}

func newServeCommand() *cobra.Command {
	var mode string
	var verbose bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Instagram MCP server via OpenAI Tunnel, Cloudflare or stdio",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (result error) {
			if mode != "openai" && mode != "stdio" && mode != "cloudflare" && mode != "cf" {
				return fmt.Errorf("unsupported transport %q (use openai, cloudflare, cf or stdio)", mode)
			}
			svc := instagram.New()
			if err := svc.Open(cmd.Context()); err != nil {
				return fmt.Errorf("restore Instagram session (run 'ig-mcp login' first): %w", err)
			}
			defer func() {
				if err := svc.Close(); err != nil {
					result = errors.Join(result, fmt.Errorf("close Instagram session: %w", err))
				}
			}()

			server := mcp.NewServer(&mcp.Implementation{Name: "instagram-mcp", Version: releaseVersion()}, nil)
			mcptools.Register(server, svc)
			return transport.RunWithOptions(cmd.Context(), server, mode, verbose)
		},
	}
	cmd.Flags().StringVar(&mode, "transport", "openai", "MCP transport: openai, cloudflare (cf) or stdio")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show safe, concise OpenAI Tunnel diagnostics")
	return cmd
}
