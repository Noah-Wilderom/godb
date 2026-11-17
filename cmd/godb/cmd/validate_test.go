package cmd

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
)

func TestRunDatabaseValidate(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "database.db")
		require.NoError(t, database.Create(path))

		if corrupt {
			db, err := database.Open(path)
			require.NoError(t, err)
			require.NoError(t, db.Put([]byte{0}, nil))
			require.NoError(t, db.Close())
		}

		var stdout, stderr bytes.Buffer

		command := &cobra.Command{}
		command.SetOut(&stdout)
		command.SetErr(&stderr)

		err := runDatabaseValidate(command, []string{path})
		if corrupt {
			require.ErrorIs(t, err, database.ErrInvalidCatalog)
			require.Empty(t, stdout.String())
		} else {
			require.NoError(t, err)
			require.Equal(t, "Database file is valid: "+path+"\n", stdout.String())
		}

		require.Empty(t, stderr.String())
	}
}
