package mysqlwire

import (
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func informationColumns(view string) []schema.Column {
	var names []string

	switch view {
	case "SCHEMATA":
		names = []string{"CATALOG_NAME", "SCHEMA_NAME", "DEFAULT_CHARACTER_SET_NAME", "DEFAULT_COLLATION_NAME", "SQL_PATH"}
	case "TABLES":
		names = []string{"TABLE_CATALOG", "TABLE_SCHEMA", "TABLE_NAME", "TABLE_TYPE", "ENGINE", "TABLE_ROWS", "AUTO_INCREMENT", "TABLE_COLLATION", "CREATE_OPTIONS", "TABLE_COMMENT"}
	case "COLUMNS":
		names = []string{"TABLE_CATALOG", "TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME", "ORDINAL_POSITION", "COLUMN_DEFAULT", "IS_NULLABLE", "DATA_TYPE", "CHARACTER_MAXIMUM_LENGTH", "CHARACTER_OCTET_LENGTH", "NUMERIC_PRECISION", "NUMERIC_SCALE", "DATETIME_PRECISION", "CHARACTER_SET_NAME", "COLLATION_NAME", "COLUMN_TYPE", "COLUMN_KEY", "EXTRA", "PRIVILEGES", "COLUMN_COMMENT", "GENERATION_EXPRESSION"}
	case "TABLE_CONSTRAINTS":
		names = []string{"CONSTRAINT_CATALOG", "CONSTRAINT_SCHEMA", "CONSTRAINT_NAME", "TABLE_SCHEMA", "TABLE_NAME", "CONSTRAINT_TYPE", "ENFORCED"}
	case "KEY_COLUMN_USAGE":
		names = []string{"CONSTRAINT_CATALOG", "CONSTRAINT_SCHEMA", "CONSTRAINT_NAME", "TABLE_CATALOG", "TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME", "ORDINAL_POSITION", "POSITION_IN_UNIQUE_CONSTRAINT", "REFERENCED_TABLE_SCHEMA", "REFERENCED_TABLE_NAME", "REFERENCED_COLUMN_NAME"}
	case "STATISTICS":
		names = []string{"TABLE_CATALOG", "TABLE_SCHEMA", "TABLE_NAME", "NON_UNIQUE", "INDEX_SCHEMA", "INDEX_NAME", "SEQ_IN_INDEX", "COLUMN_NAME", "COLLATION", "CARDINALITY", "SUB_PART", "PACKED", "NULLABLE", "INDEX_TYPE", "COMMENT", "INDEX_COMMENT", "IS_VISIBLE", "EXPRESSION"}
	default:
		return nil
	}

	columns := make([]schema.Column, len(names))
	for i, name := range names {
		columns[i] = schema.Column{Name: name, Type: record.TypeText}
		switch name {
		case "ORDINAL_POSITION", "NON_UNIQUE", "SEQ_IN_INDEX":
			columns[i].Type = record.TypeInt64
		case "TABLE_ROWS", "AUTO_INCREMENT", "CHARACTER_MAXIMUM_LENGTH", "CHARACTER_OCTET_LENGTH", "NUMERIC_PRECISION", "NUMERIC_SCALE", "DATETIME_PRECISION", "CARDINALITY", "SUB_PART", "POSITION_IN_UNIQUE_CONSTRAINT":
			columns[i].Type, columns[i].Nullable = record.TypeInt64, true
		case "SQL_PATH", "COLUMN_DEFAULT", "CHARACTER_SET_NAME", "COLLATION_NAME", "PACKED", "EXPRESSION", "REFERENCED_TABLE_SCHEMA", "REFERENCED_TABLE_NAME", "REFERENCED_COLUMN_NAME":
			columns[i].Nullable = true
		}
	}

	return columns
}

func informationRows(view, database string, tables []schema.Table, emit func([]record.Value) error) error {
	add := func(values ...any) error {
		row, err := metadataValues(values)
		if err != nil {
			return err
		}

		return emit(row)
	}
	if view == "SCHEMATA" {
		return add("def", database, "utf8mb4", "utf8mb4_bin", nil)
	}

	for _, table := range tables {
		switch view {
		case "TABLES":
			if err := add("def", database, table.Name, "BASE TABLE", "GoDB", nil, nil, "utf8mb4_bin", "", ""); err != nil {
				return err
			}
		case "COLUMNS":
			for i, column := range table.Columns {
				nullable := "NO"
				if column.Nullable {
					nullable = "YES"
				}

				var charset, collation, precision, scale any

				dataType := discoveryType(column.Type)
				switch column.Type {
				case record.TypeText:
					charset, collation = "utf8mb4", "utf8mb4_bin"
				case record.TypeInt64:
					precision, scale = int64(19), int64(0)
				case record.TypeUint64:
					dataType = "bigint"
					precision, scale = int64(20), int64(0)
				case record.TypeBool:
					dataType = "tinyint"
					precision, scale = int64(1), int64(0)
				}

				if err := add("def", database, table.Name, column.Name, int64(i+1), nil, nullable, dataType, nil, nil, precision, scale, nil, charset, collation, discoveryType(column.Type), discoveryColumnKey(table, column.ID), "", "select,insert", "", ""); err != nil {
					return err
				}
			}
		case "TABLE_CONSTRAINTS", "KEY_COLUMN_USAGE":
			columns := make(map[uint32]schema.Column, len(table.Columns))
			for _, column := range table.Columns {
				columns[column.ID] = column
			}

			for _, index := range catalogIndexes(table) {
				if !index.Unique {
					continue
				}

				if view == "TABLE_CONSTRAINTS" {
					kind := "UNIQUE"
					if index.ID == 0 {
						kind = "PRIMARY KEY"
					}

					if err := add("def", database, index.Name, database, table.Name, kind, "YES"); err != nil {
						return err
					}

					continue
				}

				for i, id := range index.ColumnIDs {
					column, ok := columns[id]
					if !ok {
						return errProtocol
					}

					if err := add("def", database, index.Name, "def", database, table.Name, column.Name, int64(i+1), nil, nil, nil, nil); err != nil {
						return err
					}
				}
			}
		case "STATISTICS":
			columns := make(map[uint32]schema.Column, len(table.Columns))
			for _, column := range table.Columns {
				columns[column.ID] = column
			}

			for _, index := range catalogIndexes(table) {
				unique := int64(1)
				if index.Unique {
					unique = 0
				}

				for i, id := range index.ColumnIDs {
					column, ok := columns[id]
					if !ok {
						return errProtocol
					}

					nullable := ""
					if column.Nullable {
						nullable = "YES"
					}

					if err := add("def", database, table.Name, unique, database, index.Name, int64(i+1), column.Name, "A", nil, nil, nil, nullable, "BTREE", "", "", "YES", nil); err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}

func metadataValues(values []any) ([]record.Value, error) {
	row := make([]record.Value, len(values))
	for i, value := range values {
		switch value := value.(type) {
		case nil:
			row[i] = record.NewNull()
		case string:
			var err error

			row[i], err = record.NewText(value)
			if err != nil {
				return nil, err
			}
		case int64:
			row[i] = record.NewInt64(value)
		default:
			return nil, errProtocol
		}
	}

	return row, nil
}

func catalogIndexes(table schema.Table) []schema.Index {
	return append([]schema.Index{{Name: "PRIMARY", Unique: true, ColumnIDs: []uint32{table.PrimaryKeyColumnID}}}, table.Indexes...)
}
