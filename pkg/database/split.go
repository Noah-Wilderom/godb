package database

import (
	"errors"
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

type nodeSplitPlan struct {
	pages      [][]byte
	separators [][]byte
}

type pageUpdate struct {
	pageID uint64
	page   []byte
}

type treeSplitPlan struct {
	updates       []pageUpdate
	appendedPages [][]byte
	header        pager.DatabaseHeader
}

func prepareLeafSplit(leaf *pager.BPlusLeaf) (*nodeSplitPlan, error) {
	leaves, err := leaf.Split()
	if err != nil {
		return nil, err
	}

	plan := &nodeSplitPlan{
		pages:      make([][]byte, len(leaves)),
		separators: make([][]byte, len(leaves)-1),
	}

	for idx, leaf := range leaves {
		page, err := pager.EncodeLeaf(leaf)
		if err != nil {
			return nil, err
		}

		plan.pages[idx] = page

		if idx > 0 {
			firstKey, hasFirstKey := leaf.FirstKey()
			if !hasFirstKey {
				return nil, fmt.Errorf("leaf %d has no first key: %w", idx, pager.ErrPagerCorruptPage)
			}

			plan.separators[idx-1] = firstKey
		}
	}

	return plan, nil
}

func prepareInternalSplit(node *pager.BPlusInternal) (*nodeSplitPlan, error) {
	internals, separators, err := node.Split()
	if err != nil {
		return nil, err
	}

	pages := make([][]byte, len(internals))

	for idx, internal := range internals {
		page, err := pager.EncodeInternal(internal)
		if err != nil {
			return nil, err
		}

		pages[idx] = page
	}

	return &nodeSplitPlan{
		pages:      pages,
		separators: separators,
	}, nil
}

func (plan *treeSplitPlan) appendPage(page []byte) uint64 {
	pageID := plan.header.PageCount

	plan.appendedPages = append(plan.appendedPages, page)

	plan.header.PageCount++
	return pageID
}

func (plan *treeSplitPlan) addSplit(pageID uint64, split *nodeSplitPlan) []uint64 {
	plan.updates = append(plan.updates, pageUpdate{
		pageID: pageID,
		page:   split.pages[0],
	})

	rightChildren := make([]uint64, len(split.pages)-1)

	for i, page := range split.pages[1:] {
		rightChildren[i] = plan.appendPage(page)
	}

	return rightChildren
}

func (db *Database) prepareTreeSplit(location *leafLocation) (*treeSplitPlan, error) {
	newSplitPlan := &treeSplitPlan{
		header: *db.header,
	}

	nodePlan, err := prepareLeafSplit(location.leaf)
	if err != nil {
		return nil, fmt.Errorf("prepare leaf split: %w", err)
	}

	if len(nodePlan.pages) < 2 {
		return nil, errors.New("there's no need to create a new root for a leaf that already fits")
	}

	childID := location.pageID
	separators := nodePlan.separators
	rightChildren := newSplitPlan.addSplit(childID, nodePlan)

	for i := len(location.parents) - 1; i >= 0; i-- {
		parentID := location.parents[i]

		parentPage, err := db.readPage(parentID)
		if err != nil {
			return nil, fmt.Errorf("read parent page %d: %w", parentID, err)
		}

		parent, err := pager.DecodeInternal(parentPage)
		if err != nil {
			return nil, fmt.Errorf("decode parent page %d: %w", parentID, err)
		}

		if err := parent.InsertSplit(childID, separators, rightChildren); err != nil {
			return nil, fmt.Errorf("insert split into parent page %d: %w", parentID, err)
		}

		updatedParentPage, err := pager.EncodeInternal(parent)
		if errors.Is(err, pager.ErrPagerPageFull) {
			parentSplit, err := prepareInternalSplit(parent)
			if err != nil {
				return nil, fmt.Errorf("prepare internal split for parent page %d: %w", parentID, err)
			}

			rightChildren = newSplitPlan.addSplit(parentID, parentSplit)
			separators = parentSplit.separators
			childID = parentID
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("encode updated parent page %d: %w", parentID, err)
		}

		newSplitPlan.updates = append(newSplitPlan.updates, pageUpdate{
			pageID: parentID,
			page:   updatedParentPage,
		})

		return newSplitPlan, nil
	}

	children := make([]uint64, 1+len(rightChildren))
	children[0] = childID
	copy(children[1:], rightChildren)

	for {
		internal, err := pager.NewBPlusInternal(separators, children)
		if err != nil {
			return nil, fmt.Errorf("create new root internal node: %w", err)
		}

		rootPage, err := pager.EncodeInternal(internal)
		if errors.Is(err, pager.ErrPagerPageFull) {
			split, err := prepareInternalSplit(internal)
			if err != nil {
				return nil, fmt.Errorf("prepare internal split for new root: %w", err)
			}

			children = make([]uint64, len(split.pages))
			for idx, page := range split.pages {
				children[idx] = newSplitPlan.appendPage(page)
			}

			separators = split.separators
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("encode new root internal node: %w", err)
		}

		newSplitPlan.header.RootPageID = newSplitPlan.appendPage(rootPage)
		return newSplitPlan, nil
	}
}

func (db *Database) writeTreeSplit(plan *treeSplitPlan) error {
	header, err := pager.EncodeHeader(&plan.header)
	if err != nil {
		return fmt.Errorf("encode new header: %w", err)
	}

	fileSize, err := db.pager.GetFileSize()
	if err != nil {
		return fmt.Errorf("get file size: %w", err)
	}

	if fileSize != db.header.PageCount*uint64(db.header.PageSize) {
		return fmt.Errorf("file size %d does not match header page count %d and page size %d: %w", fileSize, db.header.PageCount, db.header.PageSize, pager.ErrPagerCorruptPage)
	}

	for idx, page := range plan.appendedPages {
		pageID, err := db.appendPage(page)
		if err != nil {
			return fmt.Errorf("append page %d: %w", idx, err)
		}

		if pageID != db.header.PageCount+uint64(idx) {
			return fmt.Errorf("appended page %d has unexpected page ID %d, expected %d: %w", idx, pageID, db.header.PageCount+uint64(idx), pager.ErrPagerCorruptPage)
		}
	}

	for idx, update := range plan.updates {
		if err := db.writePage(update.pageID, update.page); err != nil {
			return fmt.Errorf("write updated page %d (page ID %d): %w", idx, update.pageID, err)
		}
	}

	if err := db.writePage(0, header); err != nil {
		return fmt.Errorf("write new header page: %w", err)
	}

	db.header = &plan.header
	db.counters.splitOperations.Add(1)
	return nil
}
