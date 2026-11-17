package cmd

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
)

func TestRunDatabaseBackupAndRestore(t *testing.T) {
	dir := t.TempDir()
	source, archive, destination := filepath.Join(dir, "source.db"), filepath.Join(dir, "backup"), filepath.Join(dir, "restored.db")
	require.NoError(t, database.Create(source))

	var out, errOut bytes.Buffer

	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	require.NoError(t, runDatabaseBackup(cmd, []string{source, archive}))
	require.Equal(t, "Database backup created at: "+archive+"\n", out.String())
	require.Empty(t, errOut.String())
	out.Reset()
	require.NoError(t, runDatabaseRestore(cmd, []string{archive, destination}))
	require.Equal(t, "Database restored at: "+destination+"\n", out.String())
	require.Empty(t, errOut.String())
	require.NoError(t, database.Validate(destination))
	out.Reset()
	require.Error(t, runDatabaseRestore(cmd, []string{archive, destination}))
	require.Empty(t, out.String())
	require.Error(t, runDatabaseBackup(cmd, []string{source, archive}))
	require.Empty(t, out.String())
	require.Error(t, runDatabaseBackup(cmd, []string{source + ".missing", archive}))
	require.Empty(t, out.String())
}
