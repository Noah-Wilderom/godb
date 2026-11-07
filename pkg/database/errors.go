package database

import "errors"

var (
	ErrClosed        = errors.New("database is closed")
	ErrBatchTooLarge = errors.New("write batch exceeds the entry limit")
	ErrWriteFailed   = errors.New("database stopped after a storage failure")
)
