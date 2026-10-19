package pager

const (
	databaseSignature    = "GODB"
	currentFormatVersion = 1

	headerSignatureEnd     = len(databaseSignature)           // 0:4
	headerFormatVersionEnd = headerSignatureEnd + 2           // 4:6
	headerLengthEnd        = headerFormatVersionEnd + 2       // 6:8
	headerPageSizeEnd      = headerLengthEnd + 4              // 8:12
	headerReservedEnd      = headerPageSizeEnd + 4            // 12:16
	headerRootPageIDEnd    = headerReservedEnd + uint64Size   // 16:24
	headerPageCountEnd     = headerRootPageIDEnd + uint64Size // 24:32

	headerLength = headerPageCountEnd
)

type DatabaseHeader struct {
	FormatVersion uint16
	PageSize      uint32
	RootPageID    uint64
	PageCount     uint64
}

func NewDatabaseHeader() *DatabaseHeader {
	return &DatabaseHeader{
		FormatVersion: currentFormatVersion,
		PageSize:      pagerByteSize,
		RootPageID:    1,
		PageCount:     2,
	}
}
