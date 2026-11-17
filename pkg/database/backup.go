package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

const (
	backupMagic      = "GODBBKP1"
	backupHeaderSize = 32
)

func (db *Database) Backup(path string) error {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if db.closed {
		return ErrClosed
	}

	if db.failed != nil {
		return db.failed
	}

	header, err := inspect(db.pager)
	if err != nil {
		return err
	}

	if *header != *db.header {
		return fmt.Errorf("database header changed outside writer: %w", pager.ErrPagerCorruptPage)
	}

	if err := validateContents(db.pager, header); err != nil {
		return err
	}

	return publishNewFile(path, func(file *os.File) error {
		prefix := make([]byte, backupHeaderSize)
		copy(prefix, backupMagic)
		binary.LittleEndian.PutUint64(prefix[8:16], header.PageCount*uint64(header.PageSize))
		copy(prefix[16:32], header.DatabaseID[:])
		digest := sha256.New()

		writer := io.MultiWriter(file, digest)
		if _, err := writer.Write(prefix); err != nil {
			return err
		}

		for id := uint64(0); id < header.PageCount; id++ {
			page, err := db.pager.ReadPage(id)
			if err != nil {
				return err
			}

			if len(page) != int(header.PageSize) {
				return pager.ErrPagerCorruptPage
			}

			if _, err := writer.Write(page); err != nil {
				return err
			}
		}

		_, err := file.Write(digest.Sum(nil))
		return err
	})
}

func Restore(backupPath, destination string) (err error) {
	if err := requireAbsent(destination + ".wal"); err != nil {
		return fmt.Errorf("restore requires an unused WAL path: %w", err)
	}

	archive, err := os.Open(backupPath) // #nosec G304 -- Restore reads the backup path selected by the caller.
	if err != nil {
		return err
	}

	defer func() { err = errors.Join(err, archive.Close()) }()

	info, err := archive.Stat()
	if err != nil {
		return err
	}

	prefix := make([]byte, backupHeaderSize)
	if _, err := io.ReadFull(archive, prefix); err != nil {
		return fmt.Errorf("backup header: %w: %w", ErrInvalidBackup, err)
	}

	size := binary.LittleEndian.Uint64(prefix[8:16])
	if string(prefix[:8]) != backupMagic || size < 2*walPageSize || size%walPageSize != 0 || size > math.MaxInt64-backupHeaderSize-sha256.Size {
		return ErrInvalidBackup
	}

	payloadSize := int64(size)
	if info.Size() != payloadSize+backupHeaderSize+sha256.Size {
		return ErrInvalidBackup
	}

	return publishNewFile(destination, func(file *os.File) error {
		digest := sha256.New()

		_, _ = digest.Write(prefix)
		if _, err := io.CopyN(io.MultiWriter(file, digest), archive, payloadSize); err != nil {
			return fmt.Errorf("backup payload: %w: %w", ErrInvalidBackup, err)
		}

		checksum := make([]byte, sha256.Size)
		if _, err := io.ReadFull(archive, checksum); err != nil {
			return fmt.Errorf("backup checksum: %w: %w", ErrInvalidBackup, err)
		}

		if !bytes.Equal(checksum, digest.Sum(nil)) {
			return fmt.Errorf("backup checksum mismatch: %w", ErrInvalidBackup)
		}

		if err := prepareRestoredDatabase(file.Name(), prefix[16:32]); err != nil {
			return err
		}

		return requireAbsent(destination + ".wal")
	})
}

func prepareRestoredDatabase(path string, sourceID []byte) (err error) {
	p, err := pager.OpenPager(path)
	if err != nil {
		return err
	}

	defer func() { err = errors.Join(err, p.Close()) }()

	header, err := inspect(p)
	if err != nil {
		return fmt.Errorf("backup database header: %w: %w", ErrInvalidBackup, err)
	}

	if !bytes.Equal(header.DatabaseID[:], sourceID) {
		return fmt.Errorf("backup identity mismatch: %w", ErrInvalidBackup)
	}

	if err := validateContents(p, header); err != nil {
		return fmt.Errorf("backup database contents: %w: %w", ErrInvalidBackup, err)
	}

	fresh := pager.NewDatabaseHeader()
	header.FormatVersion, header.DatabaseID = fresh.FormatVersion, fresh.DatabaseID

	page, err := pager.EncodeHeader(header)
	if err != nil {
		return err
	}

	return p.WritePage(0, page)
}

func requireAbsent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return &os.PathError{Op: "create", Path: path, Err: os.ErrExist}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

func publishNewFile(path string, write func(*os.File) error) (err error) {
	if path == "" {
		return errors.New("destination path must not be empty")
	}

	if err := requireAbsent(path); err != nil {
		return err
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".godb-*")
	if err != nil {
		return err
	}

	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, file.Close())
		}

		err = errors.Join(err, os.Remove(file.Name()))
	}()

	if err := write(file); err != nil {
		return err
	}

	if err := file.Sync(); err != nil {
		return err
	}

	err = file.Close()
	closed = true

	if err != nil {
		return err
	}

	if err := os.Link(file.Name(), path); err != nil {
		return err
	}

	return syncDirectory(path)
}
