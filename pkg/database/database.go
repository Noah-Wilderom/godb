package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/Noah-Wilderom/godb/internal/observe"
	"github.com/Noah-Wilderom/godb/pkg/pager"
)

type Database struct {
	metrics *engineMetrics
	pager   pageStore
	header  *pager.DatabaseHeader
	wal     *writeAheadLog

	mu       sync.RWMutex
	closed   bool
	failed   error
	revision uint64
	applied  LogPosition

	submitMu sync.RWMutex
	closing  bool
	writes   chan *writeRequest
	done     chan struct{}
	counters counters
}

type leafLocation struct {
	pageID  uint64
	leaf    *pager.BPlusLeaf
	parents []uint64
}

func (db *Database) ID() uuid.UUID {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.header.DatabaseID
}

func Create(path string) (err error) {
	if path == "" {
		return errors.New("database path must not be empty")
	}

	if !strings.HasSuffix(path, ".db") {
		path += ".db"
	}

	if _, err := os.Stat(path + ".wal"); err == nil {
		return fmt.Errorf("existing WAL beside new database: %w", ErrRecoveryRequired)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	header := pager.NewDatabaseHeader()

	headerPage, err := pager.EncodeHeader(header)
	if err != nil {
		return fmt.Errorf("encode database header: %w", err)
	}

	leaf, err := pager.NewBPlusLeaf(nil, nil)
	if err != nil {
		return fmt.Errorf("create root leaf: %w", err)
	}

	leafPage, err := pager.EncodeLeaf(leaf)
	if err != nil {
		return fmt.Errorf("encode root leaf: %w", err)
	}

	file, err := pager.CreatePager(path)
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close database: %w", closeErr))
		}
	}()

	if err := file.WritePage(0, headerPage); err != nil {
		return fmt.Errorf("write database header: %w", err)
	}

	if err := file.WritePage(header.RootPageID, leafPage); err != nil {
		return fmt.Errorf("write root leaf: %w", err)
	}

	if err := file.Flush(); err != nil {
		return fmt.Errorf("sync database: %w", err)
	}

	return syncDirectory(path)
}

func Open(path string) (*Database, error) {
	if path == "" {
		return nil, errors.New("database path must not be empty")
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}

	p, err := pager.OpenPager(resolved)
	if err != nil {
		return nil, err
	}

	if err := p.LockExclusive(); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: %w", ErrDatabaseLocked, err), p.Close())
	}

	wal, err := openWAL(resolved)
	if err != nil {
		return nil, errors.Join(err, p.Close())
	}

	if err := wal.recover(p); err != nil {
		return nil, errors.Join(err, wal.file.Close(), p.Close())
	}

	header, err := inspect(p)
	if err != nil {
		return nil, errors.Join(err, wal.file.Close(), p.Close())
	}

	view := &Database{pager: p, header: header}

	position, err := view.readLogPosition()
	if err != nil {
		return nil, errors.Join(err, wal.file.Close(), p.Close())
	}

	db := newDatabase(p, header)
	db.applied = position
	db.wal = wal
	return db, nil
}

func (db *Database) Put(key, value []byte) error {
	return db.PutBatch([]Entry{{Key: key, Value: value}})
}

func (db *Database) applyPut(key, value []byte, leaves map[uint64]*pager.BPlusLeaf) error {
	location, err := db.findLeafWithCache(key, leaves)
	if err != nil {
		return fmt.Errorf("find leaf: %w", err)
	}

	location.leaf.Put(key, value)

	updatedLeaf, err := pager.EncodeLeaf(location.leaf)
	if errors.Is(err, pager.ErrPagerPageFull) {
		plan, err := db.prepareTreeSplit(location)
		if err != nil {
			return fmt.Errorf("prepare tree split: %w", err)
		}

		clear(leaves)
		return db.writeTreeSplit(plan)
	}

	if err != nil {
		return fmt.Errorf("encode updated leaf: %w", err)
	}

	if err := db.writePage(location.pageID, updatedLeaf); err != nil {
		return fmt.Errorf("write updated leaf: %w", err)
	}

	return nil
}

func (db *Database) Get(key []byte) ([]byte, bool, error) {
	if db.metrics != nil {
		defer observe.Since(db.metrics.get, time.Now())
	}

	db.counters.reads.Add(1)
	db.mu.RLock()
	defer db.mu.RUnlock()

	return db.getLocked(key)
}

func (db *Database) getLocked(key []byte) ([]byte, bool, error) {
	if db.closed {
		db.counters.readErrors.Add(1)
		return nil, false, ErrClosed
	}

	if db.failed != nil {
		db.counters.readErrors.Add(1)
		return nil, false, db.failed
	}

	location, err := db.findLeaf(key)
	if err != nil {
		db.counters.readErrors.Add(1)
		return nil, false, fmt.Errorf("find leaf: %w", err)
	}

	value, _, found := location.leaf.Get(key)
	return value, found, nil
}

func (db *Database) Close() error {
	db.submitMu.Lock()

	if !db.closing {
		db.closing = true
		close(db.writes)
	}

	db.submitMu.Unlock()
	<-db.done

	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return nil
	}

	db.closed = true

	var walErr error

	if db.wal != nil {
		walErr = db.wal.file.Close()
	}

	return errors.Join(walErr, db.pager.Close())
}

func (db *Database) findLeaf(key []byte) (*leafLocation, error) {
	return db.findLeafWithCache(key, nil)
}

func (db *Database) findLeafWithCache(key []byte, leaves map[uint64]*pager.BPlusLeaf) (*leafLocation, error) {
	pageID := db.header.RootPageID
	visitedPages := make(map[uint64]bool)

	var parents []uint64

	for {
		if pageID == 0 || pageID >= db.header.PageCount {
			return nil, fmt.Errorf("invalid page ID %d: %w", pageID, pager.ErrPagerCorruptPage)
		}

		if visitedPages[pageID] {
			return nil, fmt.Errorf("cycle detected in B+ tree at page ID %d: %w", pageID, pager.ErrPagerCorruptPage)
		}

		visitedPages[pageID] = true

		if leaf := leaves[pageID]; leaf != nil {
			return &leafLocation{pageID: pageID, leaf: leaf, parents: parents}, nil
		}

		page, err := db.readPage(pageID)
		if err != nil {
			return nil, fmt.Errorf("read page %d: %w", pageID, err)
		}

		switch page[0] {
		case pager.PageTypeLeaf:
			leaf, err := pager.DecodeLeaf(page)
			if err != nil {
				return nil, fmt.Errorf("decode leaf page %d: %w", pageID, err)
			}

			if leaves != nil {
				leaves[pageID] = leaf
			}

			return &leafLocation{
				pageID:  pageID,
				leaf:    leaf,
				parents: parents,
			}, nil
		case pager.PageTypeInternal:
			internalNode, err := pager.DecodeInternal(page)
			if err != nil {
				return nil, fmt.Errorf("decode internal page %d: %w", pageID, err)
			}

			parents = append(parents, pageID)
			pageID = internalNode.ChildFor(key)
			continue

		default:
			return nil, fmt.Errorf("unknown page type %d at page ID %d: %w", page[0], pageID, pager.ErrPagerCorruptPage)
		}
	}
}
