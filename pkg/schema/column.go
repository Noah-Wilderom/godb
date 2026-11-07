package schema

import "github.com/Noah-Wilderom/godb/pkg/record"

type Column struct {
	ID       uint32
	Name     string
	Type     record.Type
	Nullable bool
}
