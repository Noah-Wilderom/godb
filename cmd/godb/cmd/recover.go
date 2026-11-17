package cmd

import (
	"github.com/spf13/cobra"

	"github.com/Noah-Wilderom/godb/pkg/database"
)

var recoverCmd = &cobra.Command{
	Use:   "recover <path>",
	Short: "Recover a pending database write-ahead log",
	Args:  cobra.ExactArgs(1),
	RunE:  runDatabaseRecover,
}

func init() { databaseCmd.AddCommand(recoverCmd) }

func runDatabaseRecover(cmd *cobra.Command, args []string) error {
	db, err := database.Open(args[0])
	if err != nil {
		return err
	}

	if err := db.Close(); err != nil {
		return err
	}

	cmd.Printf("Database recovery completed: %s\n", args[0])
	return nil
}
