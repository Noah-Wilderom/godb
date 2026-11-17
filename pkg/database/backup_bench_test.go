package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkDatabaseBackup(b *testing.B) {
	db, _ := openIdentityDatabase(b)
	require.NoError(b, db.PutBatch(crashEntries()))
	path := filepath.Join(b.TempDir(), "backup")
	b.ReportAllocs()

	for b.Loop() {
		if err := db.Backup(path); err != nil {
			b.Fatal(err)
		}

		b.StopTimer()
		require.NoError(b, os.Remove(path))
		b.StartTimer()
	}
}

func BenchmarkDatabaseRestore(b *testing.B) {
	archive, _ := backupFixture(b)
	path := filepath.Join(b.TempDir(), "restored.db")
	b.ReportAllocs()

	for b.Loop() {
		if err := Restore(archive, path); err != nil {
			b.Fatal(err)
		}

		b.StopTimer()
		require.NoError(b, os.Remove(path))
		b.StartTimer()
	}
}
