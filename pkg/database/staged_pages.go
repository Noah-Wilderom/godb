package database

import (
	"bytes"
	"fmt"
	"math"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

const (
	walPageSize = 4096
	maxWALPages = 8192
)

type pageChange struct {
	id     uint64
	page   []byte
	append bool
}

type stagedPages struct {
	pageStore

	size    uint64
	pages   map[uint64][]byte
	changes []pageChange
}

func newStagedPages(base pageStore) (*stagedPages, error) {
	size, err := base.GetFileSize()
	if err != nil {
		return nil, err
	}

	if size%walPageSize != 0 {
		return nil, fmt.Errorf("database size is not page aligned: %w", pager.ErrPagerCorruptPage)
	}

	return &stagedPages{pageStore: base, size: size, pages: make(map[uint64][]byte)}, nil
}

func (s *stagedPages) ReadPage(id uint64) ([]byte, error) {
	if page, found := s.pages[id]; found {
		return bytes.Clone(page), nil
	}

	return s.pageStore.ReadPage(id)
}

func (s *stagedPages) WritePage(id uint64, page []byte) error {
	return s.stage(id, page, false)
}

func (s *stagedPages) AppendPage(page []byte) (uint64, error) {
	id := s.size / walPageSize
	if err := s.stage(id, page, true); err != nil {
		return 0, err
	}

	s.size += walPageSize
	return id, nil
}

func (s *stagedPages) stage(id uint64, page []byte, appendPage bool) error {
	if len(page) != walPageSize || id > (math.MaxInt64-walPageSize)/walPageSize || (!appendPage && id >= s.size/walPageSize) {
		return fmt.Errorf("invalid staged page %d", id)
	}

	if len(s.changes) >= maxWALPages-1 {
		return fmt.Errorf("write group exceeds staged page limit")
	}

	owned := bytes.Clone(page)
	s.pages[id] = owned
	s.changes = append(s.changes, pageChange{id: id, page: owned, append: appendPage})
	return nil
}

func (s *stagedPages) GetFileSize() (uint64, error) { return s.size, nil }

func (db *Database) commitStaged(s *stagedPages) error {
	header, err := pager.EncodeHeader(db.header)
	if err != nil {
		return err
	}

	s.pages[0] = header
	if db.wal != nil {
		if err := db.wal.commit(s.pages, s.size); err != nil {
			return fmt.Errorf("commit WAL: %w", err)
		}
	}

	for _, change := range s.changes {
		if change.append {
			id, err := db.appendPage(change.page)
			if err != nil {
				return err
			}

			if id != change.id {
				return fmt.Errorf("appended page ID %d, expected %d", id, change.id)
			}
		} else if err := db.writePage(change.id, change.page); err != nil {
			return err
		}
	}

	if err := db.flush(); err != nil {
		return err
	}

	if db.wal != nil {
		return db.wal.clear()
	}

	return nil
}
