package cmd

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
)

func TestRunDatabaseRecover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	require.NoError(t, database.Create(path))

	var out, errOut bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	require.NoError(t, runDatabaseRecover(cmd, []string{path}))
	require.Equal(t, "Database recovery completed: "+path+"\n", out.String())
	require.Empty(t, errOut.String())
	out.Reset()
	require.Error(t, runDatabaseRecover(cmd, []string{path + ".missing"}))
	require.Empty(t, out.String())
}
