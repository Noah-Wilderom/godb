package database

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

type Database struct {
	pager  *pager.Pager
	header *pager.DatabaseHeader
}

type leafLocation struct {
	pageID  uint64
	leaf    *pager.BPlusLeaf
	parents []uint64
}

func Create(path string) (err error) {
	if path == "" {
		return errors.New("database path must not be empty")
	}

	if !strings.HasSuffix(path, ".db") {
		path += ".db"
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
	return nil
}

func Open(path string) (*Database, error) {
	if path == "" {
		return nil, errors.New("database path must not be empty")
	}

	p, err := pager.OpenPager(path)
	if err != nil {
		return nil, err
	}

	header, err := inspect(p)
	if err != nil {
		return nil, errors.Join(err, p.Close())
	}

	return &Database{
		pager:  p,
		header: header,
	}, nil
}

func (db *Database) Put(key, value []byte) error {
	location, err := db.findLeaf(key)
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

		return db.applyTreeSplit(plan)
	}
	if err != nil {
		return fmt.Errorf("encode updated leaf: %w", err)
	}

	if err := db.pager.WritePage(location.pageID, updatedLeaf); err != nil {
		return fmt.Errorf("write updated leaf: %w", err)
	}

	if err := db.pager.Flush(); err != nil {
		return fmt.Errorf("sync database: %w", err)
	}

	return nil
}

func (db *Database) Get(key []byte) ([]byte, bool, error) {
	location, err := db.findLeaf(key)
	if err != nil {
		return nil, false, fmt.Errorf("find leaf: %w", err)
	}

	value, _, found := location.leaf.Get(key)
	return value, found, nil
}

func (db *Database) Close() error {
	return db.pager.Close()
}

func (db *Database) findLeaf(key []byte) (*leafLocation, error) {
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

		page, err := db.pager.ReadPage(pageID)
		if err != nil {
			return nil, fmt.Errorf("read page %d: %w", pageID, err)
		}

		switch page[0] {
		case pager.PageTypeLeaf:
			leaf, err := pager.DecodeLeaf(page)
			if err != nil {
				return nil, fmt.Errorf("decode leaf page %d: %w", pageID, err)
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
