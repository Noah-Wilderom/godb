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
	MaxOrderColumns = 64
)

type sortColumn struct {
	column     schema.Column
	descending bool
}

type sortableRow struct {
	row  schema.Row
	keys [][]byte
}

func (p *parser) selectWindow() {
	if p.err != nil {
		return
	}

	if p.word("ORDER") {
		p.keyword("BY")

		for p.err == nil {
			if len(p.statement.orders) == MaxOrderColumns {
				p.err = fmt.Errorf("too many ORDER BY columns: %w", ErrLimit)
				return
			}

			order := ordering{column: p.name()}
			if p.word("DESC") {
				order.descending = true
			} else {
				p.word("ASC")
			}

			p.statement.orders = append(p.statement.orders, order)
			if !p.take(',') {
				break
			}
		}
	}

	if p.word("LIMIT") {
		p.statement.limit = p.limitValue()
		if p.take(',') {
			p.statement.offset = p.statement.limit
			p.statement.limit = p.limitValue()
		} else if p.word("OFFSET") {
			p.statement.offset = p.limitValue()
		}
	}
}

func (p *parser) limitValue() *expression {
	if p.peek().kind != '?' {
		token := p.peek()
		if token.kind != tokenNumber {
			p.fail("expected unsigned integer or parameter for LIMIT/OFFSET")
			return nil
		}

		for _, c := range token.text {
			if c < '0' || c > '9' {
				p.fail("LIMIT/OFFSET literals must be unsigned integers")
				return nil
			}
		}
	}

	value := p.value()
	return &value
}

func (p *Plan) prepareSelectWindow(s *Statement, columns map[string]schema.Column, args []record.Value, description *Description) error {
	seen := make(map[uint32]bool, len(s.orders))
	for _, order := range s.orders {
		column, ok := columns[order.column]
		if !ok {
			return ErrColumn
		}

		switch column.Type {
		case record.TypeInt64, record.TypeUint64, record.TypeText, record.TypeBool, record.TypeBytes, record.TypeUUID:
		default:
			return fmt.Errorf("unsupported ORDER BY type: %w", ErrType)
		}

		if !seen[column.ID] {
			p.orders = append(p.orders, sortColumn{column: column, descending: order.descending})
			seen[column.ID] = true
		}
	}

	p.limit = MaxSelectRows
	if s.limit != nil {
		count, err := prepareRowCount(*s.limit, "limit", args, description)
		if err != nil {
			return err
		}

		p.limit = count
	}

	if s.offset != nil {
		offset, err := prepareRowCount(*s.offset, "offset", args, description)
		if err != nil {
			return err
		}

		p.offset = offset
	}

	return nil
}

func prepareRowCount(expr expression, name string, args []record.Value, description *Description) (int, error) {
	if description != nil && expr.parameter >= 0 {
		description.Parameters[expr.parameter] = schema.Column{Name: name, Type: record.TypeUint64}
		return 0, nil
	}

	value, err := bind(expr, record.TypeUint64, false, args)
	if err != nil {
		return 0, err
	}

	count, err := value.Uint64()
	if err != nil {
		return 0, err
	}

	if count > MaxSelectRows {
		return 0, fmt.Errorf("%s exceeds %d: %w", name, MaxSelectRows, ErrLimit)
	}

	return int(count), nil
}

func (p *Plan) sortRows(rows []schema.Row) error {
	if len(p.orders) == 0 {
		return nil
	}

	sortable := make([]sortableRow, len(rows))
	for i, row := range rows {
		entry := sortableRow{row: row, keys: make([][]byte, len(p.orders))}
		for j, order := range p.orders {
			value, exists := row[order.column.ID]
			if !exists {
				return database.ErrInvalidData
			}

			if value.IsNull() {
				continue
			}

			if value.Type() != order.column.Type {
				return database.ErrInvalidData
			}

			if value.Type() == record.TypeBool {
				flag, err := value.Bool()
				if err != nil {
					return err
				}

				entry.keys[j] = []byte{0}
				if flag {
					entry.keys[j][0] = 1
				}
			} else {
				key, err := record.EncodeKeyValue(value)
				if err != nil {
					return err
				}

				entry.keys[j] = key
			}
		}

		sortable[i] = entry
	}

	slices.SortStableFunc(sortable, func(a, b sortableRow) int {
		for i, order := range p.orders {
			compared := bytes.Compare(a.keys[i], b.keys[i])
			if compared != 0 {
				if order.descending {
					return -compared
				}

				return compared
			}
		}

		return 0
	})

	for i, entry := range sortable {
		rows[i] = entry.row
	}

	return nil
}
