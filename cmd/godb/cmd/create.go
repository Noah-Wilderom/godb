package cmd

import (
	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/spf13/cobra"
)

var createCmd = &cobra.Command{
	Use:     "create <path>",
	Aliases: []string{"c", "new"},
	Short:   "Create a new database file",
	Long:    "Create a new database file at the given path.",
	Example: "  godb database create ./example.db",
	Args:    cobra.ExactArgs(1),
	RunE:    runDatabaseCreate,
}

func init() {
	databaseCmd.AddCommand(createCmd)
}

func runDatabaseCreate(cmd *cobra.Command, args []string) error {
	path := args[0]
	if err := database.Create(path); err != nil {
		return err
	}

	cmd.Printf("Database file created at: %s\n", path)

	return nil
}
