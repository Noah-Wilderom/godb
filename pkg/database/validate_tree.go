package database

import (
	"bytes"
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

type validationPage struct {
	id           uint64
	lower, upper []byte
	depth        uint64
}

func walkValidationTree(p pageStore, header *pager.DatabaseHeader, visit func(Entry) error) error {
	pending := []validationPage{{id: header.RootPageID}}
	seen := make(map[uint64]struct{})

	var (
		lastKey             []byte
		hasLastKey, hasLeaf bool
		leafDepth           uint64
	)

	for len(pending) > 0 {
		frame := pending[len(pending)-1]
		pending = pending[:len(pending)-1]

		if frame.id == 0 || frame.id >= header.PageCount {
			return fmt.Errorf("page ID %d outside database: %w", frame.id, pager.ErrPagerCorruptPage)
		}

		if _, exists := seen[frame.id]; exists {
			return fmt.Errorf("page %d is referenced more than once: %w", frame.id, pager.ErrPagerCorruptPage)
		}

		seen[frame.id] = struct{}{}

		page, err := p.ReadPage(frame.id)
		if err != nil {
			return fmt.Errorf("read page %d: %w", frame.id, err)
		}

		if len(page) != int(header.PageSize) {
			return fmt.Errorf("invalid size for page %d: %w", frame.id, pager.ErrPagerCorruptPage)
		}

		switch page[0] {
		case pager.PageTypeInternal:
			children, err := validationChildren(page, frame)
			if err != nil {
				return fmt.Errorf("internal page %d: %w", frame.id, err)
			}

			for i := len(children) - 1; i >= 0; i-- {
				pending = append(pending, children[i])
			}
		case pager.PageTypeLeaf:
			leaf, err := pager.DecodeLeaf(page)
			if err != nil {
				return fmt.Errorf("leaf page %d: %w", frame.id, err)
			}

			if hasLeaf && frame.depth != leafDepth {
				return fmt.Errorf("leaf page %d has inconsistent depth: %w", frame.id, pager.ErrPagerCorruptPage)
			}

			hasLeaf, leafDepth = true, frame.depth

			for i := 0; ; i++ {
				key, value, found := leaf.EntryAt(i)
				if !found {
					break
				}

				if !validationKeyInRange(key, frame) || (hasLastKey && bytes.Compare(lastKey, key) >= 0) {
					return fmt.Errorf("leaf page %d has a misplaced key: %w", frame.id, pager.ErrPagerCorruptPage)
				}

				lastKey, hasLastKey = key, true
				if err := visit(Entry{Key: key, Value: value}); err != nil {
					return fmt.Errorf("page %d key %x: %w", frame.id, key, err)
				}
			}
		default:
			return fmt.Errorf("page %d has unknown type %d: %w", frame.id, page[0], pager.ErrPagerCorruptPage)
		}
	}

	return nil
}

func validationChildren(page []byte, parent validationPage) ([]validationPage, error) {
	node, err := pager.DecodeInternal(page)
	if err != nil {
		return nil, err
	}

	ids := node.ChildrenForRange(nil, nil)
	children := make([]validationPage, len(ids))
	lower := parent.lower

	for i, id := range ids {
		upper := parent.upper
		if separator, found := node.SeparatorAt(i); found {
			if !validationKeyInRange(separator, parent) {
				return nil, fmt.Errorf("separator outside parent range: %w", pager.ErrPagerCorruptPage)
			}

			upper = separator
		}

		children[i] = validationPage{id: id, lower: lower, upper: upper, depth: parent.depth + 1}
		lower = upper
	}

	return children, nil
}

func validationKeyInRange(key []byte, frame validationPage) bool {
	return (frame.lower == nil || bytes.Compare(key, frame.lower) >= 0) && (frame.upper == nil || bytes.Compare(key, frame.upper) < 0)
}
