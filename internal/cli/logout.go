package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"go.mewis.me/ig-mcp/internal/instagram"
)

func newLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the saved local Instagram session (no remote logout)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// ForgetSession only removes the local session; it never revokes browser cookies.
			if err := instagram.New().ForgetSession(); err != nil && !errors.Is(err, instagram.ErrNoSession) {
				return errors.New("cannot remove local Instagram session (check filesystem permissions)")
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Local Instagram session cleared. Instagram and browser sessions remain active.")
			return err
		},
	}
}
