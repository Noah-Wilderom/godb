package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:           "godb",
	Short:         "A database written in Go",
	SilenceErrors: true,
	SilenceUsage:  true,
	Args:          cobra.NoArgs,
	Version:       Version,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)
}

// Execute parses arguments and runs the selected command.
// The main package prints errors and chooses the process exit code.
func Execute() error {
	return rootCmd.Execute()
}
