package sql

import "errors"

var (
	ErrSyntax           = errors.New("invalid or unsupported SQL syntax")
	ErrLimit            = errors.New("SQL resource limit exceeded")
	ErrBinding          = errors.New("invalid SQL parameter binding")
	ErrType             = errors.New("SQL value has an incompatible type or range")
	ErrIndexName        = errors.New("invalid or duplicate SQL index name")
	ErrColumn           = errors.New("unknown or repeated SQL column")
	ErrUnsupported      = errors.New("SQL operation is not supported")
	ErrTableID          = errors.New("CREATE TABLE requires an assigned nonzero table ID")
	ErrClusterExecution = errors.New("SQL local execution cannot access a Raft-bound database")
	ErrRejected         = errors.New("SQL command rejected")
)
