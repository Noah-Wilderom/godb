package pager

import (
	"bytes"
	"errors"
	"fmt"
)

func ValidateLeaf(leaf *BPlusLeaf) error {
	if leaf == nil {
		return errors.New("leaf is nil")
	}

	if err := validateLeafEntries(leaf.keys, leaf.values); err != nil {
		return err
	}

	for i := range leaf.keys {
		if len(leaf.keys[i]) > maxKeySize {
			return fmt.Errorf("entry %d: key exceeds maximum payload size: %w", i, ErrPagerEntryTooLarge)
		}

		if len(leaf.values[i]) > maxEntryPayload-len(leaf.keys[i]) {
			return fmt.Errorf("entry %d: key and value exceed page capacity: %w", i, ErrPagerEntryTooLarge)
		}
	}

	remainingSpace := pagerByteSize - leafHeaderSize
	for i := range leaf.keys {
		if remainingSpace < leafEntryHeaderSize {
			return fmt.Errorf("entry %d: not enough space for entry header: %w", i, ErrPagerPageFull)
		}
		remainingSpace -= leafEntryHeaderSize

		if remainingSpace < len(leaf.keys[i]) {
			return fmt.Errorf("entry %d: key does not fit: %w", i, ErrPagerPageFull)
		}
		remainingSpace -= len(leaf.keys[i])

		if remainingSpace < len(leaf.values[i]) {
			return fmt.Errorf("entry %d: value does not fit: %w", i, ErrPagerPageFull)
		}
		remainingSpace -= len(leaf.values[i])
	}

	return nil
}

func validateLeafEntries(keys, values [][]byte) error {
	if len(keys) != len(values) {
		return errors.New("keys and values must be the same length")
	}

	return validateKeyOrder(keys)
}

func ValidateHeader(header *DatabaseHeader) error {
	if header == nil {
		return errors.New("header is nil")
	}

	if header.FormatVersion != currentFormatVersion {
		return fmt.Errorf("unsupported database version: %d should be: %d", header.FormatVersion, currentFormatVersion)
	}

	if header.PageSize != pagerByteSize {
		return fmt.Errorf("unsupported page size: %d should be: %d", header.PageSize, pagerByteSize)
	}

	if header.PageCount < 2 {
		return errors.New("page count must be at least 2")
	}

	if header.PageCount > maxPageID+1 {
		return fmt.Errorf("page count must not exceed %d", maxPageID+1)
	}

	if header.RootPageID == 0 || header.RootPageID >= header.PageCount {
		return fmt.Errorf("root page ID must be between 1 and %d", header.PageCount-1)
	}

	return nil
}

func validateKeyOrder(keys [][]byte) error {
	for i := 1; i < len(keys); i++ {
		if bytes.Compare(keys[i], keys[i-1]) <= 0 {
			return fmt.Errorf("keys must be sorted in strictly ascending order: %w", ErrPagerInvalidArgument)
		}
	}
	return nil
}

func validateInternalEntries(keys [][]byte, children []uint64) error {
	if len(children) != len(keys)+1 {
		return ErrPagerInvalidInternalNode
	}

	if err := validateKeyOrder(keys); err != nil {
		return err
	}

	seen := make(map[uint64]struct{}, len(children))
	for i := range children {
		if _, exists := seen[children[i]]; exists {
			return ErrPagerInvalidInternalNode
		}
		seen[children[i]] = struct{}{}

		if children[i] > maxPageID || children[i] == 0 {
			return ErrPagerInvalidInternalNode
		}
	}

	return nil
}

func ValidateInternal(internal *BPlusInternal) error {
	if internal == nil {
		return fmt.Errorf("internal node is nil: %w", ErrPagerInvalidArgument)
	}

	if err := validateInternalEntries(internal.keys, internal.children); err != nil {
		return err
	}

	for i := range internal.keys {
		if len(internal.keys[i]) > maxKeySize {
			return fmt.Errorf("entry %d: key exceeds maximum payload size: %w", i, ErrPagerEntryTooLarge)
		}
	}

	remainingSpace := pagerByteSize - internalHeaderSize
	for i := range internal.keys {
		remainingSpace -= internalEntryHeaderSize + len(internal.keys[i])
		if remainingSpace < 0 {
			return fmt.Errorf("entry %d: not enough space for entry: %w", i, ErrPagerPageFull)
		}
	}

	return nil
}
