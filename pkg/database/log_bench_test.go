package database

import (
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"
)

func BenchmarkApplyLogEntry(b *testing.B) {
	for _, mode := range []string{"NewRequest", "RetryRequest", "ReplayIndex", "Noop"} {
		b.Run(mode, func(b *testing.B) {
			db, _ := openIdentityDatabase(b)
			entry := testLogEntry(b, 1, Command{Operations: []CommandOperation{{Kind: CommandPut, Key: []byte("key"), Value: []byte("value")}}})
			assertLogResult(b, db, entry, CommandOK)
			b.ReportAllocs()

			for b.Loop() {
				if mode != "ReplayIndex" {
					entry.Index++
				}

				switch mode {
				case "NewRequest":
					entry.RequestID = uuid.NewV4()
				case "Noop":
					entry.RequestID, entry.Command = uuid.Nil(), nil
				}

				result, err := db.ApplyLogEntry(entry)
				if err != nil || result.Code != CommandOK {
					b.Fatalf("result=%v err=%v", result, err)
				}
			}

			_, err := db.AppliedPosition()
			require.NoError(b, err)
		})
	}
}
