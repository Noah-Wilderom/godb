package raftstore

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
)

type faultJournal struct {
	journal

	write func([]byte, int64) (int, error)
	sync  func() error
}

func (j *faultJournal) WriteAt(data []byte, offset int64) (int, error) {
	if j.write != nil {
		return j.write(data, offset)
	}

	return j.journal.WriteAt(data, offset)
}

func (j *faultJournal) Sync() error {
	if j.sync != nil {
		return j.sync()
	}

	return j.journal.Sync()
}

func nextReady() raft.Ready {
	return raft.Ready{HardState: testHard(2, 2, 2), Entries: []*raftpb.Entry{testEntry(2, 2, "replacement")}}
}

func TestStoreRecoversEveryPartialTail(t *testing.T) {
	store, path := testStore(t)
	seedStore(t, store)
	boundary := store.offset
	require.NoError(t, store.Save(nextReady()))
	require.NoError(t, store.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	for cut := boundary; cut <= int64(len(data)); cut++ {
		t.Run(fmt.Sprint(cut-boundary), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cut.raft")
			require.NoError(t, os.WriteFile(path, data[:cut], 0o600))
			recovered, err := Open(path, testIdentity())
			require.NoError(t, err)
			defer func() { require.NoError(t, recovered.Close()) }()

			if cut < int64(len(data)) {
				assertState(t, recovered, 1, 1, 1, 3)
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.Equal(t, boundary, info.Size())
			} else {
				assertState(t, recovered, 2, 2, 2, 2)
			}

			require.NoError(t, recovered.Save(nextReady()))
		})
	}
}

func TestStoreCorruptionPreservesFile(t *testing.T) {
	store, path := testStore(t)
	seedStore(t, store)
	boundary := store.offset
	require.NoError(t, store.Save(nextReady()))
	require.NoError(t, store.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	for _, offset := range []int64{0, 8, 24, 32, headerSize, headerSize + 8, headerSize + 16, headerSize + frameHeaderSize, boundary - 33, boundary - 1, boundary + frameHeaderSize, int64(len(data)) - 1} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			corrupt := append([]byte(nil), data...)
			corrupt[offset] ^= 1
			path := filepath.Join(t.TempDir(), "bad.raft")
			require.NoError(t, os.WriteFile(path, corrupt, 0o600))
			_, err := Open(path, testIdentity())
			require.ErrorIs(t, err, ErrCorrupt)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, corrupt, after)
		})
	}
}

func TestStoreRejectsReorderedFrames(t *testing.T) {
	store, path := testStore(t)
	seedStore(t, store)
	boundary := store.offset
	require.NoError(t, store.Save(nextReady()))
	require.NoError(t, store.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	reordered := append([]byte(nil), data[:headerSize]...)
	reordered = append(reordered, data[boundary:]...)
	reordered = append(reordered, data[headerSize:boundary]...)
	require.NoError(t, os.WriteFile(path, reordered, 0o600))
	_, err = Open(path, testIdentity())
	require.ErrorIs(t, err, ErrCorrupt)
}

func TestStoreRejectsChecksummedInvalidUpdate(t *testing.T) {
	store, path := testStore(t)
	seedStore(t, store)
	payload, err := encodeUpdate(testHard(1, 2, 1), nil)
	require.NoError(t, err)
	frame, _ := encodeFrame(payload, store.digest)
	require.NoError(t, writeAt(store.file, frame, store.offset))
	require.NoError(t, store.Close())
	_, err = Open(path, testIdentity())
	require.ErrorIs(t, err, ErrCorrupt)
	require.ErrorIs(t, err, ErrInvalidUpdate)
}

func TestStoreWriteAndSyncFailures(t *testing.T) {
	for _, kind := range []string{"write", "short write", "sync"} {
		t.Run(kind, func(t *testing.T) {
			store, path := testStore(t)
			seedStore(t, store)
			base := store.file
			injected := errors.New("injected I/O failure")

			fault := &faultJournal{journal: base}
			if kind == "sync" {
				fault.sync = func() error { return injected }
			} else {
				fault.write = func(data []byte, offset int64) (int, error) {
					n, err := base.WriteAt(data[:len(data)/2], offset)
					if err != nil {
						return n, err
					}

					if kind == "short write" {
						return n, nil
					}

					return n, injected
				}
			}

			store.file = fault
			err := store.Save(nextReady())
			require.ErrorIs(t, err, ErrFailed)

			if kind == "short write" {
				require.ErrorIs(t, err, io.ErrShortWrite)
			} else {
				require.ErrorIs(t, err, injected)
			}

			require.ErrorIs(t, store.Save(raft.Ready{}), ErrFailed)
			_, _, err = store.InitialState()
			require.ErrorIs(t, err, ErrFailed)
			_, err = store.Entries(1, 2, 0)
			require.ErrorIs(t, err, ErrFailed)
			require.NoError(t, store.Close())
			recovered, err := Open(path, testIdentity())
			require.NoError(t, err)
			defer func() { require.NoError(t, recovered.Close()) }()

			if kind == "sync" {
				assertState(t, recovered, 2, 2, 2, 2)
			} else {
				assertState(t, recovered, 1, 1, 1, 3)
			}

			require.NoError(t, recovered.Save(nextReady()))
		})
	}
}

func TestStoreCrashRecovery(t *testing.T) {
	for _, phase := range []string{"partial", "synced", "acknowledged"} {
		t.Run(phase, func(t *testing.T) {
			store, path := testStore(t)
			seedStore(t, store)
			require.NoError(t, store.Close())
			command := exec.Command(os.Args[0], "-test.run=^TestStoreCrashChild$")
			command.Env = append(os.Environ(), "GODB_RAFT_CRASH_PATH="+path, "GODB_RAFT_CRASH_PHASE="+phase)
			output, err := command.CombinedOutput()

			var exited *exec.ExitError

			require.ErrorAs(t, err, &exited, "%s", output)
			require.Equal(t, 77, exited.ExitCode(), "%s", output)
			recovered, err := Open(path, testIdentity())
			require.NoError(t, err)
			defer func() { require.NoError(t, recovered.Close()) }()

			if phase == "partial" {
				assertState(t, recovered, 1, 1, 1, 3)
			} else {
				assertState(t, recovered, 2, 2, 2, 2)
			}
		})
	}
}

func TestStoreCrashChild(t *testing.T) {
	path := os.Getenv("GODB_RAFT_CRASH_PATH")
	if path == "" {
		t.Skip("crash subprocess only")
	}

	store, err := Open(path, testIdentity())
	require.NoError(t, err)
	base := store.file

	switch os.Getenv("GODB_RAFT_CRASH_PHASE") {
	case "partial":
		store.file = &faultJournal{journal: base, write: func(data []byte, offset int64) (int, error) {
			_, err := base.WriteAt(data[:len(data)/2], offset)
			require.NoError(t, err)
			require.NoError(t, base.Sync())
			os.Exit(77)
			return 0, nil
		}}
	case "synced":
		store.file = &faultJournal{journal: base, sync: func() error {
			require.NoError(t, base.Sync())
			os.Exit(77)
			return nil
		}}
	}

	require.NoError(t, store.Save(nextReady()))
	os.Exit(77)
}

func FuzzDecodeUpdate(f *testing.F) {
	payload, err := encodeUpdate(testHard(1, 1, 1), []*raftpb.Entry{testEntry(1, 1, "one")})
	if err != nil {
		f.Fatal(err)
	}

	f.Add(payload)
	f.Add([]byte{})
	bad := make([]byte, updateHeaderSize)
	binary.LittleEndian.PutUint32(bad[24:], ^uint32(0))
	f.Add(bad)
	f.Fuzz(func(t *testing.T, data []byte) {
		hard, entries, err := decodeUpdate(data)
		if err != nil {
			return
		}

		encoded, err := encodeUpdate(hard, entries)
		require.NoError(t, err)
		_, _, err = decodeUpdate(encoded)
		require.NoError(t, err)
	})
}
