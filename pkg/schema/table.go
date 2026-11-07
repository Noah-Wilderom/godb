package schema

type Table struct {
	ID                 uint32
	Name               string
	Columns            []Column
	PrimaryKeyColumnID uint32
	Indexes            []Index
}
