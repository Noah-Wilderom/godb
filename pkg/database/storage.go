package database

import "fmt"

type pageStore interface {
	ReadPage(uint64) ([]byte, error)
	WritePage(uint64, []byte) error
	AppendPage([]byte) (uint64, error)
	GetFileSize() (uint64, error)
	Flush() error
	Close() error
}

func (db *Database) readPage(id uint64) ([]byte, error) {
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

	db.counters.pageWrites.Add(1)
	return nil
}

func (db *Database) appendPage(page []byte) (uint64, error) {
	id, err := db.pager.AppendPage(page)
	if err == nil {
		db.counters.pageAppends.Add(1)
		db.counters.pageWrites.Add(1)
	}

	return id, err
}

func (db *Database) flush() error {
	if err := db.pager.Flush(); err != nil {
		db.counters.syncErrors.Add(1)
		return fmt.Errorf("sync database: %w", err)
	}

	db.counters.syncs.Add(1)
	return nil
}
