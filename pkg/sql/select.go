package sql

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

const (
	MaxSelectRows = database.MaxScanEntries
	MaxPredicates = 64
)

type rowFilter struct {
	operation predicateOperator
	columnID  uint32
	value     record.Value
}

func (p *Plan) lookupValues() map[uint32]record.Value {
	values := make(map[uint32]record.Value, len(p.filters))
	for _, filter := range p.filters {
		if filter.operation != predicateEqual && filter.operation != predicateIsNull {
			continue
		}

		if _, exists := values[filter.columnID]; !exists {
			values[filter.columnID] = filter.value
		}
	}

	return values
}

func (p *Plan) chooseIndex() {
	values := p.lookupValues()

	if key, exists := values[p.table.PrimaryKeyColumnID]; exists {
		p.keys = []record.Value{key}
		return
	}

	var best *schema.Index

	bestPrefix := 0

	for i := range p.table.Indexes {
		candidate := &p.table.Indexes[i]
		prefix := 0

		for _, id := range candidate.ColumnIDs {
			if _, exists := values[id]; !exists {
				break
			}

			prefix++
		}

		if prefix == 0 {
			continue
		}

		if best == nil || betterIndex(*candidate, prefix, *best, bestPrefix, values) {
			best = candidate
			bestPrefix = prefix
		}
	}

	if best == nil {
		p.tableScan = true
		return
	}

	p.indexID = best.ID

	p.unique = uniqueLookup(*best, bestPrefix, values)
	for _, id := range best.ColumnIDs[:bestPrefix] {
		p.keys = append(p.keys, values[id])
	}
}

func betterIndex(candidate schema.Index, prefix int, current schema.Index, currentPrefix int, values map[uint32]record.Value) bool {
	candidatePoint := uniqueLookup(candidate, prefix, values)

	currentPoint := uniqueLookup(current, currentPrefix, values)
	if candidatePoint != currentPoint {
		return candidatePoint
	}

	if prefix != currentPrefix {
		return prefix > currentPrefix
	}

	if len(candidate.ColumnIDs) != len(current.ColumnIDs) {
		return len(candidate.ColumnIDs) < len(current.ColumnIDs)
	}

	return candidate.ID < current.ID
}

func equalityMatches(a, b record.Value) bool {
	if a.IsNull() || b.IsNull() || a.Type() != b.Type() {
		return false
	}

	switch a.Type() {
	case record.TypeInt64, record.TypeUint64, record.TypeText, record.TypeBool, record.TypeUUID:
		return a == b
	case record.TypeBytes:
		left, leftErr := a.Bytes()
		right, rightErr := b.Bytes()
		return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
	default:
		return false
	}
}

func (p *Plan) matches(row schema.Row) (bool, error) {
	matches := true

	for _, filter := range p.filters {
		value, exists := row[filter.columnID]
		if !exists {
			return false, database.ErrInvalidData
		}

		switch filter.operation {
		case predicateIsNull:
			matches = matches && value.IsNull()
		case predicateIsNotNull:
			matches = matches && !value.IsNull()
		case predicateEqual:
			matches = matches && equalityMatches(value, filter.value)
		default:
			matched, err := filter.comparisonMatches(value)
			if err != nil {
				return false, err
			}

			matches = matches && matched
		}
	}

	return matches, nil
}

func (p *Plan) Read(reader RowReader) (Result, error) {
	if p != nil && p.kind == Explain && p.explained != nil {
		return p.explainResult(), nil
	}

	if p == nil || p.kind != Select || reader == nil {
		return Result{}, ErrUnsupported
	}

	result := Result{Columns: slices.Clone(p.columns), Rows: make([][]record.Value, 0)}
	if p.noMatch {
		if p.count != nil {
			return p.countResult(result, nil)
		}

		return result, nil
	}

	rows, err := p.readRows(reader)
	if err != nil {
		return Result{}, err
	}

	matched := rows[:0]
	for _, row := range rows {
		matches, err := p.matches(row)
		if err != nil {
			return Result{}, err
		}

		if !matches {
			continue
		}

		matched = append(matched, row)
	}

	if p.count != nil {
		return p.countResult(result, matched)
	}

	if err := p.sortRows(matched); err != nil {
		return Result{}, err
	}

	start := min(p.offset, len(matched))

	end := start + min(p.limit, len(matched)-start)
	for _, row := range matched[start:end] {
		values := make([]record.Value, len(p.columns))
		for i, column := range p.columns {
			value, exists := row[column.ID]
			if !exists {
				return Result{}, database.ErrInvalidData
			}

			values[i] = value
		}

		result.Rows = append(result.Rows, values)
	}

	return result, nil
}

func (p *Plan) AccessPath() string {
	if p == nil || p.kind != Select {
		return ""
	}

	if p.secondaryRange != nil {
		return "index_range"
	}

	if p.primaryRange != nil {
		return "primary_range"
	}

	if p.tableScan {
		return "table_scan"
	}

	if p.indexID == 0 {
		return "primary"
	}

	if p.unique {
		return "unique"
	}

	return "index_scan"
}

func (p *Plan) readRows(reader RowReader) ([]schema.Row, error) {
	if p.secondaryRange != nil {
		page, err := reader.ScanRowsByIndexRangePage(p.table, p.indexID, p.secondaryRange.lower, p.secondaryRange.upper, MaxSelectRows, nil, p.keys...)
		if err != nil {
			return nil, err
		}

		if page.Next != nil {
			return nil, fmt.Errorf("SELECT index range exceeds %d candidate rows: %w", MaxSelectRows, ErrLimit)
		}

		return page.Rows, nil
	}

	if p.primaryRange != nil {
		page, err := reader.ScanRowsRangePage(p.table, p.primaryRange.lower, p.primaryRange.upper, MaxSelectRows, nil)
		if err != nil {
			return nil, err
		}

		if page.Next != nil {
			return nil, fmt.Errorf("SELECT primary range exceeds %d candidate rows: %w", MaxSelectRows, ErrLimit)
		}

		return page.Rows, nil
	}

	if p.tableScan {
		page, err := reader.ScanRowsPage(p.table, MaxSelectRows, nil)
		if err != nil {
			return nil, err
		}

		if page.Next != nil {
			return nil, fmt.Errorf("SELECT table scan exceeds %d candidate rows: %w", MaxSelectRows, ErrLimit)
		}

		return page.Rows, nil
	}

	if p.indexID != 0 && !p.unique {
		page, err := reader.ScanRowsByIndexPage(p.table, p.indexID, MaxSelectRows, nil, p.keys...)
		if err != nil {
			return nil, err
		}

		if page.Next != nil {
			return nil, fmt.Errorf("SELECT index scan exceeds %d candidate rows: %w", MaxSelectRows, ErrLimit)
		}

		return page.Rows, nil
	}

	var (
		row   schema.Row
		found bool
		err   error
	)

	if p.indexID == 0 {
		row, found, err = reader.GetRow(p.table, p.keys[0])
	} else {
		row, found, err = reader.GetRowByUniqueIndex(p.table, p.indexID, p.keys...)
	}

	if err != nil || !found {
		return nil, err
	}

	return []schema.Row{row}, nil
}
