package pager

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

func EncodeLeaf(leaf *BPlusLeaf) ([]byte, error) {
	if err := ValidateLeaf(leaf); err != nil {
		return nil, err
	}

	buff := make([]byte, pagerByteSize)
	buff[0] = PageTypeLeaf

	binary.LittleEndian.PutUint16(buff[1:leafHeaderSize], uint16(len(leaf.keys)))

	offset := leafHeaderSize
	for i := 0; i < len(leaf.keys); i++ {
		keyLengthStart := offset
		valueLengthStart := keyLengthStart + uint16Size
		keyStart := offset + leafEntryHeaderSize
		valueStart := keyStart + len(leaf.keys[i])
		entryEnd := valueStart + len(leaf.values[i])

		binary.LittleEndian.PutUint16(buff[keyLengthStart:valueLengthStart], uint16(len(leaf.keys[i])))
		binary.LittleEndian.PutUint16(buff[valueLengthStart:keyStart], uint16(len(leaf.values[i])))

		copy(buff[keyStart:valueStart], leaf.keys[i])
		copy(buff[valueStart:entryEnd], leaf.values[i])

		offset = entryEnd
	}

	return buff, nil
}

func DecodeLeaf(page []byte) (*BPlusLeaf, error) {
	if len(page) != pagerByteSize {
		return nil, fmt.Errorf("page size is %d bytes, expected %d: %w", len(page), pagerByteSize, ErrPagerCorruptPage)
	}

	if page[0] != PageTypeLeaf {
		return nil, fmt.Errorf("page type %d is not leaf: %w", page[0], ErrPagerCorruptPage)
	}

	entryCount := binary.LittleEndian.Uint16(page[1:leafHeaderSize])
	if entryCount > maxLeafEntries {
		return nil, fmt.Errorf("entry count %d exceeds maximum of %d: %w", entryCount, maxLeafEntries, ErrPagerCorruptPage)
	}

	keys := make([][]byte, entryCount)
	values := make([][]byte, entryCount)

	offset := leafHeaderSize
	for i := 0; i < int(entryCount); i++ {
		if len(page)-offset < leafEntryHeaderSize {
			return nil, fmt.Errorf("entry %d: truncated entry header: %w", i, ErrPagerCorruptPage)
		}

		keyLengthStart := offset
		valueLengthStart := keyLengthStart + uint16Size
		keyStart := offset + leafEntryHeaderSize

		keyLength := binary.LittleEndian.Uint16(page[keyLengthStart:valueLengthStart])
		valueLength := binary.LittleEndian.Uint16(page[valueLengthStart:keyStart])

		if len(page)-keyStart < int(keyLength) {
			return nil, fmt.Errorf("entry %d: key length %d exceeds remaining page bytes: %w", i, keyLength, ErrPagerCorruptPage)
		}

		valueStart := keyStart + int(keyLength)
		if len(page)-valueStart < int(valueLength) {
			return nil, fmt.Errorf("entry %d: value length %d exceeds remaining page bytes: %w", i, valueLength, ErrPagerCorruptPage)
		}

		entryEnd := valueStart + int(valueLength)

		keys[i] = bytes.Clone(page[keyStart:valueStart])
		values[i] = bytes.Clone(page[valueStart:entryEnd])

		offset = entryEnd
	}

	leaf := &BPlusLeaf{keys: keys, values: values}

	if err := ValidateLeaf(leaf); err != nil {
		return nil, fmt.Errorf("invalid decoded leaf: %v: %w", err, ErrPagerCorruptPage)
	}

	return leaf, nil
}

func EncodeHeader(header *DatabaseHeader) ([]byte, error) {
	if err := ValidateHeader(header); err != nil {
		return nil, err
	}

	buff := make([]byte, pagerByteSize)

	copy(buff[0:len(databaseSignature)], databaseSignature)

	binary.LittleEndian.PutUint16(buff[headerSignatureEnd:headerFormatVersionEnd], header.FormatVersion)
	binary.LittleEndian.PutUint16(buff[headerFormatVersionEnd:headerLengthEnd], uint16(headerLength))
	binary.LittleEndian.PutUint32(buff[headerLengthEnd:headerPageSizeEnd], header.PageSize)
	// reserved zeros: buff[headerPageSizeEnd:headerReservedEnd]
	binary.LittleEndian.PutUint64(buff[headerReservedEnd:headerRootPageIDEnd], header.RootPageID)
	binary.LittleEndian.PutUint64(buff[headerRootPageIDEnd:headerPageCountEnd], header.PageCount)

	return buff, nil
}

func DecodeHeader(page []byte) (*DatabaseHeader, error) {
	if len(page) != pagerByteSize {
		return nil, fmt.Errorf("page size is not %d bytes", pagerByteSize)
	}

	for _, b := range page[headerPageSizeEnd:headerReservedEnd] {
		if b != 0 {
			return nil, fmt.Errorf("reserved header bytes must be zero")
		}
	}

	for _, b := range page[headerLength:] {
		if b != 0 {
			return nil, fmt.Errorf("reserved header bytes must be zero")
		}
	}

	if string(page[0:len(databaseSignature)]) != databaseSignature {
		return nil, fmt.Errorf("invalid database signature")
	}

	if binary.LittleEndian.Uint16(page[headerFormatVersionEnd:headerLengthEnd]) != uint16(headerLength) {
		return nil, fmt.Errorf("invalid header length")
	}

	header := &DatabaseHeader{
		FormatVersion: binary.LittleEndian.Uint16(page[headerSignatureEnd:headerFormatVersionEnd]),
		PageSize:      binary.LittleEndian.Uint32(page[headerLengthEnd:headerPageSizeEnd]),
		RootPageID:    binary.LittleEndian.Uint64(page[headerReservedEnd:headerRootPageIDEnd]),
		PageCount:     binary.LittleEndian.Uint64(page[headerRootPageIDEnd:headerPageCountEnd]),
	}

	if err := ValidateHeader(header); err != nil {
		return nil, err
	}

	return header, nil
}

func EncodeInternal(node *BPlusInternal) ([]byte, error) {
	if err := ValidateInternal(node); err != nil {
		return nil, err
	}

	buff := make([]byte, pagerByteSize)
	buff[0] = PageTypeInternal

	countEnd := 1 + uint16Size
	binary.LittleEndian.PutUint16(buff[1:countEnd], uint16(len(node.keys)))
	binary.LittleEndian.PutUint64(buff[countEnd:internalHeaderSize], node.children[0])

	offset := internalHeaderSize
	for i := range node.keys {
		keyLengthStart := offset
		keyStart := keyLengthStart + uint16Size
		childStart := keyStart + len(node.keys[i])
		entryEnd := childStart + uint64Size

		binary.LittleEndian.PutUint16(buff[keyLengthStart:keyStart], uint16(len(node.keys[i])))
		copy(buff[keyStart:childStart], node.keys[i])
		binary.LittleEndian.PutUint64(buff[childStart:entryEnd], node.children[i+1])

		offset = entryEnd
	}

	return buff, nil
}

func DecodeInternal(page []byte) (*BPlusInternal, error) {
	if len(page) != pagerByteSize {
		return nil, fmt.Errorf("page size is not %d bytes: %w", pagerByteSize, ErrPagerCorruptPage)
	}

	if page[0] != PageTypeInternal {
		return nil, fmt.Errorf("page type %d is not internal: %w", page[0], ErrPagerCorruptPage)
	}

	countEnd := 1 + uint16Size
	entryCount := binary.LittleEndian.Uint16(page[1:countEnd])

	if entryCount > maxInternalEntries {
		return nil, fmt.Errorf("entry count %d exceeds maximum of %d: %w", entryCount, maxInternalEntries, ErrPagerCorruptPage)
	}

	keys := make([][]byte, entryCount)
	children := make([]uint64, entryCount+1)

	offset := internalHeaderSize
	children[0] = binary.LittleEndian.Uint64(page[countEnd:offset])

	for i := range keys {
		if len(page)-offset < internalEntryHeaderSize {
			return nil, fmt.Errorf("entry %d: truncated internal entry: %w", i, ErrPagerCorruptPage)
		}

		keyLengthStart := offset
		keyStart := offset + uint16Size

		keyLength := binary.LittleEndian.Uint16(page[keyLengthStart:keyStart])
		if int(keyLength) > len(page)-keyStart-uint64Size {
			return nil, fmt.Errorf("entry %d: key length %d exceeds remaining page bytes: %w", i, keyLength, ErrPagerCorruptPage)
		}

		childStart := keyStart + int(keyLength)
		entryEnd := childStart + uint64Size

		if entryEnd > len(page) {
			return nil, fmt.Errorf("entry %d: child pointer exceeds remaining page bytes: %w", i, ErrPagerCorruptPage)
		}

		keys[i] = bytes.Clone(page[keyStart:childStart])
		children[i+1] = binary.LittleEndian.Uint64(page[childStart:entryEnd])

		offset = entryEnd
	}

	internalNode := &BPlusInternal{keys: keys, children: children}

	if err := ValidateInternal(internalNode); err != nil {
		return nil, fmt.Errorf("invalid decoded internal node: %v: %w", err, ErrPagerCorruptPage)
	}

	return internalNode, nil
}
