package pager

import "fmt"

type ErrorCode string

type Error struct {
	Code    ErrorCode
	Message string
}

var (
	ErrPagerPageFull = &Error{
		Code:    CodePagerPageFull,
		Message: "the page is full and cannot accommodate additional entries",
	}
	ErrPagerEntryTooLarge = &Error{
		Code:    CodePagerEntryTooLarge,
		Message: "the entry size exceeds the maximum allowed size for a page",
	}
	ErrPagerInvalidArgument = &Error{
		Code:    CodePagerInvalidArgument,
		Message: "invalid argument provided to pager operation",
	}
	ErrPagerInvalidPageID = &Error{
		Code:    CodePagerInvalidPageID,
		Message: "the provided page ID is invalid or out of range",
	}
	ErrPagerCorruptPage = &Error{
		Code:    CodePagerCorruptPage,
		Message: "the page data is corrupt and cannot be read or written",
	}
	ErrPagerUnsupportedFormat = &Error{
		Code:    CodePagerUnsupportedFormat,
		Message: "the page format is unsupported or unrecognized",
	}

	ErrPagerInvalidInternalNode = &Error{
		Code:    CodePagerInvalidInternalNode,
		Message: "the internal node structure is invalid, possibly due to mismatched keys and children",
	}
)

const (
	CodePagerPageFull            ErrorCode = "PAGER_PAGE_FULL"
	CodePagerEntryTooLarge       ErrorCode = "PAGER_ENTRY_TOO_LARGE"
	CodePagerInvalidArgument     ErrorCode = "PAGER_INVALID_ARGUMENT"
	CodePagerInvalidPageID       ErrorCode = "PAGER_INVALID_PAGE_ID"
	CodePagerCorruptPage         ErrorCode = "PAGER_CORRUPT_PAGE"
	CodePagerUnsupportedFormat   ErrorCode = "PAGER_UNSUPPORTED_FORMAT"
	CodePagerInvalidInternalNode ErrorCode = "PAGER_INVALID_INTERNAL_NODE"
)

func (e *Error) Error() string {
	return fmt.Sprintf("GODB [%s]: %s", e.Code, e.Message)
}
