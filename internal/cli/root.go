package cli

import "github.com/spf13/cobra"

func NewRootCommand(version string) *cobra.Command {
	setLanguageFromEnvironment()
	cmd := &cobra.Command{
		Use:           "steward",
		Short:         tr("Discover, understand, and safely clean up cloud resources", "发现、理解并安全清理云资源"),
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newServerCommand(version))
	cmd.AddCommand(newUpdateCommand(version))
	localizeCobra(cmd)
	return cmd
}
