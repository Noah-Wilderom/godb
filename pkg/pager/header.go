package pager

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"uuid"
)

const (
	databaseSignature    = "GODB"
	currentFormatVersion = 2

	headerSignatureEnd     = len(databaseSignature)           // 0:4
	headerFormatVersionEnd = headerSignatureEnd + 2           // 4:6
	headerLengthEnd        = headerFormatVersionEnd + 2       // 6:8
	headerPageSizeEnd      = headerLengthEnd + 4              // 8:12
	headerReservedEnd      = headerPageSizeEnd + 4            // 12:16
	headerRootPageIDEnd    = headerReservedEnd + uint64Size   // 16:24
	headerPageCountEnd     = headerRootPageIDEnd + uint64Size // 24:32

	headerLength              = headerPageCountEnd
	headerIdentityEnd         = headerLength + 16
	headerIdentityChecksumEnd = headerIdentityEnd + sha256.Size
)

type DatabaseHeader struct {
	FormatVersion uint16
	PageSize      uint32
	RootPageID    uint64
	PageCount     uint64
	DatabaseID    uuid.UUID
}

func NewDatabaseHeader() *DatabaseHeader {
	return &DatabaseHeader{
		FormatVersion: currentFormatVersion,
		PageSize:      pagerByteSize,
		RootPageID:    1,
		PageCount:     2,
		DatabaseID:    uuid.NewV4(),
	}
}

func DecodeHeaderIdentity(page []byte) (uuid.UUID, error) {
	if len(page) != pagerByteSize {
		return uuid.Nil(), fmt.Errorf("invalid identity page size")
	}

	var id uuid.UUID

	copy(id[:], page[headerLength:headerIdentityEnd])

	digest := sha256.Sum256(id[:])
	if id == uuid.Nil() || !bytes.Equal(digest[:], page[headerIdentityEnd:headerIdentityChecksumEnd]) {
		return uuid.Nil(), fmt.Errorf("invalid database identity checksum")
	}

	return id, nil
}
