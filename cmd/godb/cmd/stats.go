package cmd

import (
	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:     "stats <path>",
	Short:   "Get database statistics",
	Long:    "Get statistics about a database file.",
	Example: "  godb database stats ./example.db",
	Args:    cobra.ExactArgs(1),
	RunE:    runDatabaseStats,
}

func init() {
	databaseCmd.AddCommand(statsCmd)
}

func runDatabaseStats(cmd *cobra.Command, args []string) error {
	if err := database.Validate(args[0]); err != nil {
		return err
	}

	cmd.Printf("Database stats is not implemented yet")
	return nil
}
