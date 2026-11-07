package schema

type Index struct {
	ID        uint32
	Name      string
	ColumnIDs []uint32
	Unique    bool
}

type IndexEntry struct {
	Key                 []byte
	Value               []byte
	RequiresUniqueCheck bool
}
