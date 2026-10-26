package cli

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// Version, Commit and Date are set at build time by GoReleaser ldflags.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

func releaseVersion() string {
	if version := strings.TrimSpace(Version); version != "" {
		return version
	}
	return "dev"
}

func versionText() string {
	result := fmt.Sprintf("ig-mcp %s (%s/%s, %s)", releaseVersion(), runtime.GOOS, runtime.GOARCH, runtime.Version())
	if Commit != "" {
		result += " commit " + Commit
	}
	if Date != "" {
		result += " built " + Date
	}
	return result
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ig-mcp build version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), versionText())
			return err
		},
	}
}
