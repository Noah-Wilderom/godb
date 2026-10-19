package pager

import (
	"bytes"
	"errors"
	"slices"
)

type BPlusInternal struct {
	keys     [][]byte
	children []uint64
}

func NewBPlusInternal(keys [][]byte, children []uint64) (*BPlusInternal, error) {
	if err := validateInternalEntries(keys, children); err != nil {
		return nil, err
	}

	clonedKeys := make([][]byte, len(keys))
	for i, key := range keys {
		clonedKeys[i] = slices.Clone(key)
	}

	clonedChildren := make([]uint64, len(children))
	copy(clonedChildren, children)

	return &BPlusInternal{
		keys:     clonedKeys,
		children: clonedChildren,
	}, nil
}

func (n *BPlusInternal) ChildFor(key []byte) uint64 {
	low := 0
	high := len(n.keys) - 1

	for low <= high {
		middle := low + (high-low)/2

		if bytes.Equal(n.keys[middle], key) {
			return n.children[middle+1]
		}

		if bytes.Compare(key, n.keys[middle]) < 0 {
			high = middle - 1
		} else {
			low = middle + 1
		}
	}

	return n.children[low]
}

func (n *BPlusInternal) InsertSplit(childID uint64, separators [][]byte, rightChildren []uint64) error {
	if len(separators) < 1 || len(separators) != len(rightChildren) {
		return ErrPagerInvalidArgument
	}

	if childID == 0 || childID > maxPageID {
		return ErrPagerInvalidArgument
	}

	for i, child := range n.children {
		if child == childID {
			keys := make([][]byte, 0, len(n.keys)+len(separators))
			keys = append(keys, n.keys[:i]...)
			keys = append(keys, separators...)
			keys = append(keys, n.keys[i:]...)

			children := make([]uint64, 0, len(n.children)+len(rightChildren))
			children = append(children, n.children[:i+1]...)
			children = append(children, rightChildren...)
			children = append(children, n.children[i+1:]...)

			newInternal, err := NewBPlusInternal(keys, children)
			if err != nil {
				return err
			}

			n.keys = newInternal.keys
			n.children = newInternal.children

			return nil
		}
	}

	return ErrPagerInvalidArgument
}

func (n *BPlusInternal) Split() ([]*BPlusInternal, [][]byte, error) {
	if err := ValidateInternal(n); err != nil && !errors.Is(err, ErrPagerPageFull) {
		return nil, nil, err
	}

	start := 0
	remainingSpace := pagerByteSize - internalHeaderSize

	var (
		internals  []*BPlusInternal
		separators [][]byte
	)
	for i := range n.keys {
		entrySize := internalEntryHeaderSize + len(n.keys[i])
		if remainingSpace < entrySize {
			internal, err := NewBPlusInternal(n.keys[start:i], n.children[start:i+1])
			if err != nil {
				return nil, nil, err
			}

			internals = append(internals, internal)
			separators = append(separators, slices.Clone(n.keys[i]))
			start = i + 1
			remainingSpace = pagerByteSize - internalHeaderSize
			continue
		}

		remainingSpace -= entrySize
	}

	internal, err := NewBPlusInternal(n.keys[start:], n.children[start:])
	if err != nil {
		return nil, nil, err
	}
	internals = append(internals, internal)
	return internals, separators, nil
}
