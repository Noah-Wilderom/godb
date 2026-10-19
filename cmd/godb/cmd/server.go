package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the database server",
	Long:  "Start the database server.\n\nThe server is not implemented yet.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return errors.New("server is not implemented yet")
	},
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
