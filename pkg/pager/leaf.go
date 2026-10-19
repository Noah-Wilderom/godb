package pager

import (
	"bytes"
	"errors"
	"slices"
)

type BPlusLeaf struct {
	keys   [][]byte
	values [][]byte
}

func NewBPlusLeaf(keys, values [][]byte) (*BPlusLeaf, error) {
	if err := validateLeafEntries(keys, values); err != nil {
		return nil, err
	}

	clonedKeys := make([][]byte, len(keys))
	for i, key := range keys {
		clonedKeys[i] = slices.Clone(key)
	}

	clonedValues := make([][]byte, len(values))
	for i, value := range values {
		clonedValues[i] = slices.Clone(value)
	}

	return &BPlusLeaf{
		keys:   clonedKeys,
		values: clonedValues,
	}, nil
}

func (l *BPlusLeaf) Get(key []byte) ([]byte, int, bool) {
	low := 0
	high := len(l.keys) - 1

	for low <= high {
		middle := low + (high-low)/2

		if bytes.Equal(l.keys[middle], key) {
			return bytes.Clone(l.values[middle]), middle, true
		}

		if bytes.Compare(key, l.keys[middle]) < 0 {
			high = middle - 1
		} else {
			low = middle + 1
		}
	}

	return nil, 0, false
}

func (l *BPlusLeaf) Put(key, value []byte) {
	low := 0
	high := len(l.keys) - 1

	for low <= high {
		middle := low + (high-low)/2

		if bytes.Equal(l.keys[middle], key) {
			l.values[middle] = bytes.Clone(value)
			return
		}

		if bytes.Compare(key, l.keys[middle]) > 0 {
			low = middle + 1
		} else {
			high = middle - 1
		}
	}

	l.keys = append(l.keys, nil)
	l.values = append(l.values, nil)

	copy(l.keys[low+1:], l.keys[low:len(l.keys)-1])
	copy(l.values[low+1:], l.values[low:len(l.values)-1])

	l.keys[low] = bytes.Clone(key)
	l.values[low] = bytes.Clone(value)
}

func (l *BPlusLeaf) Split() ([]*BPlusLeaf, error) {
	if err := ValidateLeaf(l); err != nil && !errors.Is(err, ErrPagerPageFull) {
		return nil, err
	}

	start := 0
	remainingSpace := pagerByteSize - leafHeaderSize

	var leaves []*BPlusLeaf
	for i := range l.keys {
		entrySize := leafEntryHeaderSize + len(l.keys[i]) + len(l.values[i])
		if entrySize > remainingSpace {
			leaf, err := NewBPlusLeaf(l.keys[start:i], l.values[start:i])
			if err != nil {
				return nil, err
			}

			leaves = append(leaves, leaf)
			start = i
			remainingSpace = pagerByteSize - leafHeaderSize
		}

		remainingSpace -= entrySize
	}

	leaf, err := NewBPlusLeaf(l.keys[start:], l.values[start:])
	if err != nil {
		return nil, err
	}
	leaves = append(leaves, leaf)

	return leaves, nil
}

func (l *BPlusLeaf) FirstKey() ([]byte, bool) {
	if len(l.keys) == 0 {
		return nil, false
	}

	return slices.Clone(l.keys[0]), true
}
