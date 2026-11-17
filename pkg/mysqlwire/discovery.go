package mysqlwire

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

const (
	MaxCatalogTables = 1024
	maxResultRows    = 4096
	maxResultBytes   = 4 << 20
)

func resultLimit() error {
	return &Error{1105, "HY000", "Result exceeds GoDB resource limits"}
}

func (s *statementSession) discovery(ctx context.Context, statement *discoveryStatement, describe bool, args []record.Value) (result sql.Result, err error) {
	if !describe {
		s.endpoint.discoveryCommands.Add(1)
		defer func() {
			if err != nil {
				s.endpoint.discoveryErrors.Add(1)
			}
		}()
	}

	if statement.database != "" && statement.database != s.endpoint.config.Database {
		return result, &Error{1049, "42000", "Unknown database"}
	}

	pattern := statement.pattern

	if !describe {
		count := 0
		if statement.parameter {
			count = 1
		}

		if len(args) != count {
			return result, &Error{1210, "HY000", "Incorrect discovery parameter count"}
		}

		if statement.parameter {
			pattern, err = args[0].Text()
			if err != nil || len(pattern) > 64 || strings.ContainsRune(pattern, '\\') {
				return result, &Error{1366, "HY000", "Discovery pattern must be UTF-8 text of at most 64 bytes without backslash escapes"}
			}
		}
	}

	var tables []schema.Table

	if statement.kind != "databases" {
		if s.endpoint.query.Catalog == nil {
			return result, sessionUnsupported()
		}

		tables, err = s.endpoint.query.Catalog(ctx, statement.table)
		if err != nil {
			return result, err
		}

		if len(tables) > MaxCatalogTables {
			return result, resultLimit()
		}

		if statement.table != "" && (len(tables) != 1 || tables[0].Name != statement.table) {
			return result, &Error{1146, "42S02", "Table does not exist"}
		}
	}

	result.Columns = discoveryColumns(statement, s.endpoint.config.Database)
	if describe {
		return result, nil
	}

	matches := func(name string) bool {
		if !statement.filtered {
			return true
		}

		if statement.exact {
			return name == pattern
		}

		return discoveryLike(name, pattern)
	}
	add := func(values ...any) error {
		if len(result.Rows) >= maxResultRows {
			return resultLimit()
		}

		row, err := metadataValues(values)
		if err != nil {
			return err
		}

		result.Rows = append(result.Rows, row)
		return nil
	}

	switch statement.kind {
	case "databases":
		if matches(s.endpoint.config.Database) {
			err = add(s.endpoint.config.Database)
		}
	case "tables":
		tables = slices.Clone(tables)
		slices.SortFunc(tables, func(a, b schema.Table) int { return strings.Compare(a.Name, b.Name) })

		for _, table := range tables {
			if !matches(table.Name) {
				continue
			}

			if statement.full {
				err = add(table.Name, "BASE TABLE")
			} else {
				err = add(table.Name)
			}

			if err != nil {
				return result, err
			}
		}
	case "columns":
		table := tables[0]
		for _, column := range table.Columns {
			if !matches(column.Name) {
				continue
			}

			nullable := "NO"
			if column.Nullable {
				nullable = "YES"
			}

			key := discoveryColumnKey(table, column.ID)

			if statement.full {
				var collation any

				if column.Type == record.TypeText {
					collation = "utf8mb4_bin"
				}

				err = add(column.Name, discoveryType(column.Type), collation, nullable, key, nil, "", "select,insert", "")
			} else {
				err = add(column.Name, discoveryType(column.Type), nullable, key, nil, "")
			}

			if err != nil {
				return result, err
			}
		}
	case "indexes":
		table := tables[0]
		indexes := catalogIndexes(table)

		columns := make(map[uint32]schema.Column, len(table.Columns))
		for _, column := range table.Columns {
			columns[column.ID] = column
		}

		for _, index := range indexes {
			nonUnique := int64(1)
			if index.Unique {
				nonUnique = 0
			}

			for i, id := range index.ColumnIDs {
				column, ok := columns[id]
				if !ok {
					return result, errProtocol
				}

				nullable := ""
				if column.Nullable {
					nullable = "YES"
				}

				err = add(table.Name, nonUnique, index.Name, int64(i+1), column.Name, "A", nil, nil, nil, nullable, "BTREE", "", "", "YES", nil)
				if err != nil {
					return result, err
				}
			}
		}
	}

	return result, err
}

func discoveryColumns(s *discoveryStatement, database string) []schema.Column {
	var names []string

	switch s.kind {
	case "databases":
		names = []string{"Database"}
	case "tables":
		names = []string{"Tables_in_" + database}
		if s.full {
			names = append(names, "Table_type")
		}
	case "columns":
		names = []string{"Field", "Type", "Null", "Key", "Default", "Extra"}
		if s.full {
			names = []string{"Field", "Type", "Collation", "Null", "Key", "Default", "Extra", "Privileges", "Comment"}
		}
	case "indexes":
		names = []string{"Table", "Non_unique", "Key_name", "Seq_in_index", "Column_name", "Collation", "Cardinality", "Sub_part", "Packed", "Null", "Index_type", "Comment", "Index_comment", "Visible", "Expression"}
	}

	columns := make([]schema.Column, len(names))
	for i, name := range names {
		columns[i] = schema.Column{Name: name, Type: record.TypeText}
		switch name {
		case "Non_unique", "Seq_in_index":
			columns[i].Type = record.TypeInt64
		case "Cardinality", "Sub_part":
			columns[i].Type, columns[i].Nullable = record.TypeInt64, true
		case "Default", "Collation", "Packed", "Expression":
			columns[i].Nullable = true
		}
	}

	return columns
}

func discoveryColumnKey(table schema.Table, id uint32) string {
	if table.PrimaryKeyColumnID == id {
		return "PRI"
	}

	key := ""

	for _, index := range table.Indexes {
		if len(index.ColumnIDs) == 0 || index.ColumnIDs[0] != id {
			continue
		}

		if index.Unique && len(index.ColumnIDs) == 1 {
			return "UNI"
		}

		key = "MUL"
	}

	return key
}

func discoveryType(typ record.Type) string {
	switch typ {
	case record.TypeInt64:
		return "bigint"
	case record.TypeUint64:
		return "bigint unsigned"
	case record.TypeBool:
		return "tinyint(1)"
	case record.TypeText:
		return "text"

	case record.TypeBytes:
		return "godb_bytes"
	case record.TypeFloat64:
		return "godb_float64"
	case record.TypeFloat32:
		return "godb_float32"
	case record.TypeJSON:
		return "godb_json"
	case record.TypeDate:
		return "godb_date"
	case record.TypeTime:
		return "godb_time"
	case record.TypeDateTime:
		return "godb_datetime"
	case record.TypeUUID:
		return "godb_uuid"
	case record.TypeDecimal:
		return "godb_decimal"
	case record.TypeMap:
		return "godb_map"
	case record.TypeTimestamp:
		return "godb_timestamp"
	case record.TypeDuration:
		return "godb_duration"
	case record.TypeArray:
		return "godb_array"
	default:
		return fmt.Sprintf("godb_type_%02x", byte(typ))
	}
}

func discoveryLike(name, pattern string) bool {
	value := []rune(name)
	matched := make([]bool, len(value)+1)

	matched[0] = true
	for _, c := range pattern {
		previous := matched[0]

		matched[0] = matched[0] && c == '%'
		for i := 1; i <= len(value); i++ {
			old := matched[i]
			if c == '%' {
				matched[i] = matched[i] || matched[i-1]
			} else {
				matched[i] = previous && (c == '_' || c == value[i-1])
			}

			previous = old
		}
	}

	return matched[len(value)]
}
