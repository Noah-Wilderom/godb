package pager

import "math"

const (
	pagerByteSize = 4096

	PageTypeLeaf     byte = 0x01
	PageTypeInternal byte = 0x02

	uint16Size          = 2
	leafEntryHeaderSize = uint16Size * 2
	uint64Size          = 8
	leafHeaderSize      = 3
	maxLeafEntries      = (pagerByteSize - leafHeaderSize) / leafEntryHeaderSize
	maxEntryPayload     = pagerByteSize - leafHeaderSize - leafEntryHeaderSize

	maxPageID = (math.MaxInt64 - pagerByteSize) / pagerByteSize

	internalEntryHeaderSize = uint16Size + uint64Size
	internalHeaderSize      = 1 + uint16Size + uint64Size
	maxInternalEntries      = (pagerByteSize - internalHeaderSize) / internalEntryHeaderSize

	maxKeySize = pagerByteSize - internalHeaderSize - internalEntryHeaderSize
)
