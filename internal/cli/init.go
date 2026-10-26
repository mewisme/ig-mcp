package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

func newInitCommand(example string) *cobra.Command {
	init := &cobra.Command{Use: "init", Short: "Initialize local configuration", RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	}}
	var directory string
	env := &cobra.Command{
		Use:   "env [filename]",
		Short: "Create an Instagram MCP dotenv file (default: .env.local)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ".env.local"
			if len(args) > 0 {
				name = args[0]
			}
			if name == "" || name == "." || name == ".." || name != filepath.Base(name) ||
				strings.ContainsAny(name, "/\\<>:\"|?*\x00") {
				return fmt.Errorf("invalid environment filename %q; use a filename without directory separators", name)
			}
			if err := os.MkdirAll(directory, 0755); err != nil {
				return fmt.Errorf("create destination directory: %w", err)
			}
			path, err := filepath.Abs(filepath.Join(directory, name))
			if err != nil {
				return fmt.Errorf("resolve destination path: %w", err)
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%s already exists; choose a different filename or path", path)
			}
			if err != nil {
				return fmt.Errorf("create environment file: %w", err)
			}
			if _, err := f.WriteString(example); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return fmt.Errorf("write environment template: %w", err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close environment file: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created %s\nRun: ig-mcp --env-path %s serve\n", path, quoteEnvPath(path))
			return err
		},
	}
	env.Flags().StringVarP(&directory, "path", "p", ".", "Directory to create the environment file in")
	init.AddCommand(env)
	return init
}

func quoteEnvPath(path string) string {
	if runtime.GOOS == "windows" {
		// PowerShell single quotes preserve spaces, dollar signs and backslashes.
		return "'" + strings.ReplaceAll(path, "'", "''") + "'"
	}
	if strings.ContainsAny(path, " \t\n\r\"'&;|$`()[]{}<>\\?*~!#") {
		return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
	}
	return path
}
