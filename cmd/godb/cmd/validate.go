package cmd

import (
	"github.com/spf13/cobra"

	"github.com/Noah-Wilderom/godb/pkg/database"
)

var validateCmd = &cobra.Command{
	Use:     "validate <path>",
	Short:   "Validate a database file",
	Long:    "Check a database file for invalid pages.",
	Example: "  godb database validate ./example.db",
	Args:    cobra.ExactArgs(1),
	RunE:    runDatabaseValidate,
}

func init() {
	databaseCmd.AddCommand(validateCmd)
}

func runDatabaseValidate(cmd *cobra.Command, args []string) error {
	if err := database.Validate(args[0]); err != nil {
		return err
	}

	cmd.Printf("Database file is valid: %s\n", args[0])
	return nil
}
