package sql

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type (
	Catalog interface {
		GetTableByName(string) (schema.Table, bool, error)
	}
	RowReader interface {
		ScanRowsByIndexRangePage(schema.Table, uint32, *database.KeyBound, *database.KeyBound, int, *database.ScanCursor, ...record.Value) (database.RowPage, error)
		ScanRowsRangePage(schema.Table, *database.KeyBound, *database.KeyBound, int, *database.ScanCursor) (database.RowPage, error)
		ScanRowsPage(schema.Table, int, *database.ScanCursor) (database.RowPage, error)
		GetRow(schema.Table, record.Value) (schema.Row, bool, error)
		GetRowByUniqueIndex(schema.Table, uint32, ...record.Value) (schema.Row, bool, error)
		ScanRowsByIndexPage(schema.Table, uint32, int, *database.ScanCursor, ...record.Value) (database.RowPage, error)
	}
)

type PrepareOptions struct{ CreateTableID uint32 }

type Result struct {
	Columns      []schema.Column
	Rows         [][]record.Value
	AffectedRows uint64
	MatchedRows  uint64
}

type Plan struct {
	explained      *Plan
	secondaryRange *keyRange
	primaryRange   *keyRange
	count          *countPlan
	kind           Kind
	table          schema.Table
	columns        []schema.Column
	keys           []record.Value
	filters        []rowFilter
	orders         []sortColumn
	limit, offset  int
	command        []byte
	indexID        uint32
	unique         bool
	tableScan      bool
	noMatch        bool
}

func (p *Plan) Kind() Kind { return p.kind }

func Prepare(catalog Catalog, query string, options PrepareOptions, args ...record.Value) (*Plan, error) {
	statement, err := Parse(query)
	if err != nil {
		return nil, err
	}

	return prepare(catalog, statement, options, args)
}

func prepare(catalog Catalog, s *Statement, options PrepareOptions, args []record.Value) (*Plan, error) {
	return prepareStatement(catalog, s, options, args, nil)
}

type Description struct {
	Parameters []schema.Column
	Columns    []schema.Column
}

func Describe(catalog Catalog, query string) (Description, error) {
	s, err := Parse(query)
	if err != nil {
		return Description{}, err
	}

	description := Description{Parameters: make([]schema.Column, s.parameters)}

	options := PrepareOptions{}
	if s.kind == CreateTable {
		options.CreateTableID = 1
	}

	plan, err := prepareStatement(catalog, s, options, nil, &description)
	if err != nil {
		return Description{}, err
	}

	description.Columns = slices.Clone(plan.columns)
	return description, nil
}

func prepareStatement(catalog Catalog, s *Statement, options PrepareOptions, args []record.Value, description *Description) (*Plan, error) {
	if description == nil && len(args) != s.parameters {
		return nil, fmt.Errorf("expected %d parameters, got %d: %w", s.parameters, len(args), ErrBinding)
	}

	if s.kind == Explain {
		selectStatement := *s
		selectStatement.kind = Select

		selected, err := prepareStatement(catalog, &selectStatement, options, args, description)
		if err != nil {
			return nil, err
		}

		return &Plan{kind: Explain, explained: selected, columns: explainColumns()}, nil
	}

	plan := &Plan{kind: s.kind}
	resolve := func(expr expression, column schema.Column) (record.Value, error) {
		if description != nil && expr.parameter >= 0 {
			description.Parameters[expr.parameter] = column
			return record.NewNull(), nil
		}

		return bind(expr, column.Type, column.Nullable, args)
	}

	if s.kind == CreateTable {
		return prepareCreate(s, options.CreateTableID)
	}

	if options.CreateTableID != 0 {
		return nil, fmt.Errorf("table ID option is only valid for CREATE TABLE: %w", ErrBinding)
	}

	if catalog == nil {
		return nil, fmt.Errorf("catalog is required: %w", ErrBinding)
	}

	table, found, err := catalog.GetTableByName(s.table)
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, database.ErrTableNotFound
	}

	if err := schema.ValidateTable(table); err != nil {
		return nil, fmt.Errorf("invalid catalog table: %w", err)
	}

	table.Columns = slices.Clone(table.Columns)

	table.Indexes = slices.Clone(table.Indexes)
	for i := range table.Indexes {
		table.Indexes[i].ColumnIDs = slices.Clone(table.Indexes[i].ColumnIDs)
	}

	plan.table = table

	byName := make(map[string]schema.Column, len(table.Columns))
	for _, column := range table.Columns {
		byName[column.Name] = column
	}

	if s.kind == Delete {
		return plan.prepareDelete(s, byName, resolve, description != nil)
	}

	if s.kind == Update {
		return plan.prepareUpdate(s, byName, resolve, description != nil)
	}

	if s.kind == Insert {
		row := make(schema.Row, len(table.Columns))

		for i, name := range s.columns {
			column, ok := byName[name]
			if !ok {
				return nil, ErrColumn
			}

			if _, duplicate := row[column.ID]; duplicate {
				return nil, ErrColumn
			}

			value, err := resolve(s.values[i], column)
			if err != nil {
				return nil, fmt.Errorf("column %q: %w", name, err)
			}

			row[column.ID] = value
		}

		for _, column := range table.Columns {
			if _, exists := row[column.ID]; exists {
				continue
			}

			if !column.Nullable {
				return nil, fmt.Errorf("missing required column %q: %w", column.Name, ErrBinding)
			}

			row[column.ID] = record.NewNull()
		}

		if description != nil {
			return plan, nil
		}

		plan.command, err = database.EncodeCommand(database.Command{Operations: []database.CommandOperation{{Kind: database.CommandInsertRow, Table: &table, Row: row}}})
		if err != nil {
			return nil, err
		}

		return plan, nil
	}

	for _, predicate := range s.predicates {
		column, ok := byName[predicate.column]
		if !ok {
			return nil, ErrColumn
		}

		if predicate.operation == predicateIsNull || predicate.operation == predicateIsNotNull {
			plan.filters = append(plan.filters, rowFilter{columnID: column.ID, operation: predicate.operation, value: record.NewNull()})
			if predicate.operation == predicateIsNull && !column.Nullable {
				plan.noMatch = true
			}

			continue
		}

		switch column.Type {
		case record.TypeInt64, record.TypeUint64, record.TypeText, record.TypeBool, record.TypeBytes, record.TypeUUID:
		default:
			return nil, fmt.Errorf("unsupported comparison type: %w", ErrType)
		}

		column.Nullable = true

		value, err := resolve(predicate.value, column)
		if err != nil {
			return nil, err
		}

		plan.filters = append(plan.filters, rowFilter{columnID: column.ID, operation: predicate.operation, value: value})
		if value.IsNull() {
			plan.noMatch = true
		}
	}

	plan.chooseIndex()

	if err := plan.preparePrimaryRange(); err != nil {
		return nil, err
	}

	if err := plan.prepareSecondaryRange(); err != nil {
		return nil, err
	}

	if s.count != nil {
		if err := plan.prepareCount(s, byName); err != nil {
			return nil, err
		}
	} else if s.all {
		plan.columns = slices.Clone(table.Columns)
	} else {
		for _, name := range s.columns {
			column, ok := byName[name]
			if !ok {
				return nil, ErrColumn
			}

			plan.columns = append(plan.columns, column)
		}
	}

	if err := plan.prepareSelectWindow(s, byName, args, description); err != nil {
		return nil, err
	}

	return plan, nil
}

func prepareCreate(s *Statement, id uint32) (*Plan, error) {
	if id == 0 {
		return nil, ErrTableID
	}

	table := schema.Table{ID: id, Name: s.table}
	columnID := uint32(1)

	for _, definition := range s.definitions {
		column := definition.column
		column.ID = columnID
		columnID++

		if column.Name == s.primaryKey {
			if definition.explicitNull {
				return nil, fmt.Errorf("primary key cannot explicitly allow NULL: %w", ErrType)
			}

			column.Nullable = false
			table.PrimaryKeyColumnID = column.ID
		}

		table.Columns = append(table.Columns, column)
	}

	indexes, err := prepareIndexes(s.indexes, table.Columns)
	if err != nil {
		return nil, err
	}

	table.Indexes = indexes

	if err := schema.ValidateTable(table); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrType, err)
	}

	command, err := database.EncodeCommand(database.Command{Operations: []database.CommandOperation{{Kind: database.CommandCreateTable, Table: &table}}})
	if err != nil {
		return nil, err
	}

	return &Plan{kind: CreateTable, table: table, command: command}, nil
}

func bind(expression expression, typ record.Type, nullable bool, args []record.Value) (record.Value, error) {
	var value record.Value

	if expression.parameter >= 0 {
		value = args[expression.parameter]
	} else {
		t := expression.literal
		switch t.kind {
		case tokenString:
			value, _ = record.NewText(t.text)
		case tokenNumber:
			if typ == record.TypeUint64 {
				number, err := strconv.ParseUint(strings.TrimPrefix(t.text, "+"), 10, 64)
				if err != nil {
					return record.Value{}, ErrType
				}

				value = record.NewUint64(number)
			} else {
				number, err := strconv.ParseInt(t.text, 10, 64)
				if err != nil {
					return record.Value{}, ErrType
				}

				value = record.NewInt64(number)
			}
		case tokenWord:
			if strings.EqualFold(t.text, "NULL") {
				value = record.NewNull()
			} else {
				value = record.NewBool(strings.EqualFold(t.text, "TRUE"))
			}
		default:
			return record.Value{}, ErrType
		}
	}

	if value.IsNull() {
		if !nullable {
			return record.Value{}, ErrType
		}

		return value, nil
	}

	if value.Type() == typ {
		return value, nil
	}

	if value.Type() == record.TypeInt64 {
		number, err := value.Int64()
		if err != nil {
			return record.Value{}, ErrType
		}

		if typ == record.TypeUint64 && number >= 0 {
			return record.NewUint64(uint64(number)), nil
		}

		if typ == record.TypeBool && (number == 0 || number == 1) {
			return record.NewBool(number == 1), nil
		}
	}

	if value.Type() == record.TypeUint64 && typ == record.TypeInt64 {
		number, err := value.Uint64()
		if err == nil && number <= math.MaxInt64 {
			return record.NewInt64(int64(number)), nil
		}
	}

	return record.Value{}, ErrType
}

func (p *Plan) Command() ([]byte, error) {
	if p == nil || len(p.command) == 0 {
		return nil, ErrUnsupported
	}

	return bytes.Clone(p.command), nil
}
