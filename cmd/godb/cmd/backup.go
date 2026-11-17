package cmd

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/Noah-Wilderom/godb/pkg/database"
)

var backupCmd = &cobra.Command{
	Use:   "backup <database> <backup>",
	Short: "Create a verified database backup",
	Args:  cobra.ExactArgs(2),
	RunE:  runDatabaseBackup,
}

var restoreCmd = &cobra.Command{
	Use:   "restore <backup> <database>",
	Short: "Restore a backup to a new database file",
	Args:  cobra.ExactArgs(2),
	RunE:  runDatabaseRestore,
}

func init() {
	databaseCmd.AddCommand(backupCmd, restoreCmd)
}

func runDatabaseBackup(cmd *cobra.Command, args []string) error {
	db, err := database.Open(args[0])
	if err != nil {
		return err
	}

	if err := errors.Join(db.Backup(args[1]), db.Close()); err != nil {
		return err
	}

	cmd.Printf("Database backup created at: %s\n", args[1])
	return nil
}

func runDatabaseRestore(cmd *cobra.Command, args []string) error {
	if err := database.Restore(args[0], args[1]); err != nil {
		return err
	}

	cmd.Printf("Database restored at: %s\n", args[1])
	return nil
}
