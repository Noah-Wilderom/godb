package database

import (
	"fmt"
	"time"

	"github.com/Noah-Wilderom/godb/internal/observe"
)

type pageStore interface {
	ReadPage(uint64) ([]byte, error)
	WritePage(uint64, []byte) error
	AppendPage([]byte) (uint64, error)
	GetFileSize() (uint64, error)
	Flush() error
	Close() error
}

func (db *Database) readPage(id uint64) ([]byte, error) {
	if staged, ok := db.pager.(*stagedPages); ok && staged.pages[id] != nil {
		return staged.ReadPage(id)
	}

	page, err := db.pager.ReadPage(id)
	if err == nil {
		db.counters.pageReads.Add(1)
	}

	return page, err
}

func (db *Database) writePage(id uint64, page []byte) error {
	if err := db.pager.WritePage(id, page); err != nil {
		return err
	}

	if _, staged := db.pager.(*stagedPages); !staged {
		db.counters.pageWrites.Add(1)
	}

	return nil
}

func (db *Database) appendPage(page []byte) (uint64, error) {
	id, err := db.pager.AppendPage(page)

	_, staged := db.pager.(*stagedPages)
	if err == nil && !staged {
		db.counters.pageAppends.Add(1)
		db.counters.pageWrites.Add(1)
	}

	return id, err
}

func (db *Database) flush() error {
	if db.metrics != nil {
		defer observe.Since(db.metrics.sync, time.Now())
	}

	if err := db.pager.Flush(); err != nil {
		db.counters.syncErrors.Add(1)
		return fmt.Errorf("sync database: %w", err)
	}

	db.counters.syncs.Add(1)

	if db.metrics != nil {
		db.metrics.bytes.Set(float64(db.header.PageCount * uint64(db.header.PageSize)))
	}

	return nil
}
