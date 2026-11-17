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
	"slices"
	"time"
	"uuid"

	"github.com/Noah-Wilderom/godb/internal/observe"
	"github.com/Noah-Wilderom/godb/pkg/pager"
)

const (
	walMagic       = "GODBWAL2"
	walCommit      = "COMMIT01"
	walHeaderSize  = 48
	walRecordSize  = 8 + walPageSize
	walTrailerSize = 8 + sha256.Size
)

type journalFile interface {
	ReadAt([]byte, int64) (int, error)
	WriteAt([]byte, int64) (int, error)
	Stat() (os.FileInfo, error)
	Truncate(int64) error
	Sync() error
	Close() error
}

type writeAheadLog struct {
	file    journalFile
	metrics *walMetrics
}

func openWAL(path string) (*writeAheadLog, error) {
	file, err := os.OpenFile(path+".wal", os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- WAL belongs to the database path chosen by the caller.
	if err != nil {
		return nil, err
	}

	if err := syncDirectory(path); err != nil {
		return nil, errors.Join(err, file.Close())
	}

	return &writeAheadLog{file: file, metrics: newWALMetrics()}, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path)) // #nosec G304 -- Sync the caller-selected database's parent directory.
	if err != nil {
		return err
	}

	return errors.Join(dir.Sync(), dir.Close())
}

func (w *writeAheadLog) clear() error {
	if err := w.file.Truncate(0); err != nil {
		return err
	}

	return w.sync()
}

func (w *writeAheadLog) commit(pages map[uint64][]byte, size uint64) error {
	if len(pages) == 0 || len(pages) > maxWALPages || size%walPageSize != 0 || size > math.MaxInt64 {
		return ErrInvalidWAL
	}

	header, err := pager.DecodeHeader(pages[0])
	if err != nil || header.PageCount*uint64(header.PageSize) != size {
		return ErrInvalidWAL
	}

	ids := make([]uint64, 0, len(pages))
	for id, page := range pages {
		if id >= size/walPageSize || len(page) != walPageSize {
			return ErrInvalidWAL
		}

		ids = append(ids, id)
	}

	slices.Sort(ids)
	encoded := make([]byte, walHeaderSize, walHeaderSize+len(ids)*walRecordSize+walTrailerSize)
	copy(encoded, walMagic)
	binary.LittleEndian.PutUint64(encoded[8:16], size)
	binary.LittleEndian.PutUint64(encoded[16:24], uint64(len(ids)))
	copy(encoded[32:48], header.DatabaseID[:])

	for _, id := range ids {
		encoded = binary.LittleEndian.AppendUint64(encoded, id)
		encoded = append(encoded, pages[id]...)
	}

	digest := sha256.Sum256(encoded)
	encoded = append(encoded, walCommit...)
	encoded = append(encoded, digest[:]...)

	if err := w.clear(); err != nil {
		return err
	}

	n, err := w.file.WriteAt(encoded, 0)
	if err != nil {
		return err
	}

	if n != len(encoded) {
		return io.ErrShortWrite
	}

	return w.sync()
}

func (w *writeAheadLog) sync() error {
	if w.metrics != nil {
		defer observe.Since(w.metrics.sync, time.Now())
	}

	err := w.file.Sync()
	if err != nil && w.metrics != nil {
		w.metrics.errors.Inc()
	}

	return err
}

func (w *writeAheadLog) recover(p pageStore) error {
	info, err := w.file.Stat()
	if err != nil {
		return err
	}

	if info.Size() == 0 {
		return nil
	}

	if info.Size() < 32 {
		return w.clear()
	}

	header := make([]byte, 32)
	if _, err := w.file.ReadAt(header, 0); err != nil {
		return err
	}

	headerSize := walHeaderSize

	switch string(header[:8]) {
	case "GODBWAL1":
		headerSize = 32
	case walMagic:
	default:
		return ErrInvalidWAL
	}

	count := binary.LittleEndian.Uint64(header[16:24])

	size := binary.LittleEndian.Uint64(header[8:16])
	if count == 0 || count > maxWALPages || size > math.MaxInt64 || size < 2*walPageSize || size%walPageSize != 0 || binary.LittleEndian.Uint64(header[24:]) != 0 {
		return ErrInvalidWAL
	}

	expected := int64(headerSize) + int64(count)*walRecordSize + walTrailerSize
	if info.Size() < expected {
		return w.clear()
	}

	if info.Size() != expected {
		return ErrInvalidWAL
	}

	encoded := make([]byte, int(expected))
	if _, err := w.file.ReadAt(encoded, 0); err != nil {
		return err
	}

	end := len(encoded) - walTrailerSize

	digest := sha256.Sum256(encoded[:end])
	if string(encoded[end:end+8]) != walCommit || !bytes.Equal(encoded[end+8:], digest[:]) {
		return ErrInvalidWAL
	}

	pages, err := decodeWALPages(encoded[headerSize:end], size)
	if err != nil {
		return err
	}

	var id uuid.UUID

	if headerSize == walHeaderSize {
		copy(id[:], encoded[32:48])
	}

	loggedHeader, err := pager.DecodeHeader(pages[0].page)
	if err != nil || loggedHeader.DatabaseID != id {
		return ErrInvalidWAL
	}

	current, err := p.ReadPage(0)
	if err != nil {
		return err
	}

	if id != uuid.Nil() {
		currentID, err := pager.DecodeHeaderIdentity(current)
		if err != nil || currentID != id {
			return errors.Join(ErrWALIdentityMismatch, err)
		}
	} else if !bytes.Equal(current[32:80], make([]byte, 48)) {
		return ErrWALIdentityMismatch
	}

	actualSize, err := p.GetFileSize()
	if err != nil {
		return err
	}

	if actualSize > size || size/walPageSize-actualSize/walPageSize > count {
		return ErrInvalidWAL
	}

	available := make(map[uint64]bool, len(pages))
	for _, change := range pages {
		available[change.id] = true
	}

	for id := actualSize / walPageSize; id < size/walPageSize; id++ {
		if !available[id] {
			return ErrInvalidWAL
		}
	}

	for _, change := range pages {
		if err := p.WritePage(change.id, change.page); err != nil {
			return fmt.Errorf("recover page %d: %w", change.id, err)
		}
	}

	if err := p.Flush(); err != nil {
		return err
	}

	return w.clear()
}

func decodeWALPages(encoded []byte, size uint64) ([]pageChange, error) {
	if len(encoded) == 0 || len(encoded)%walRecordSize != 0 {
		return nil, ErrInvalidWAL
	}

	var pages []pageChange

	for offset := 0; offset < len(encoded); offset += walRecordSize {
		id := binary.LittleEndian.Uint64(encoded[offset : offset+8])
		page := encoded[offset+8 : offset+walRecordSize]

		if id >= size/walPageSize || (len(pages) > 0 && id <= pages[len(pages)-1].id) {
			return nil, ErrInvalidWAL
		}

		if len(pages) == 0 {
			header, err := pager.DecodeHeader(page)
			if id != 0 || err != nil || header.PageCount*uint64(header.PageSize) != size {
				return nil, ErrInvalidWAL
			}
		} else {
			var err error

			switch page[0] {
			case pager.PageTypeLeaf:
				_, err = pager.DecodeLeaf(page)
			case pager.PageTypeInternal:
				_, err = pager.DecodeInternal(page)
			default:
				err = ErrInvalidWAL
			}

			if err != nil {
				return nil, fmt.Errorf("WAL page %d: %w: %w", id, ErrInvalidWAL, err)
			}
		}

		pages = append(pages, pageChange{id: id, page: page})
	}

	return pages, nil
}
