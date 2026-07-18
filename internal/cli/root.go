package cli

import "github.com/spf13/cobra"

func NewRootCommand(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "steward",
		Short:         "Steward local server",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newServerCommand(version))
	return cmd
}
