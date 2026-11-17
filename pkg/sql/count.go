package sql

import (
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type countExpression struct {
	all           bool
	column, alias string
}

type countPlan struct{ columnID uint32 }

func (p *parser) countProjection() {
	p.keyword("COUNT")
	p.expect('(')

	count := &countExpression{all: p.take('*')}
	if !count.all {
		if p.word("DISTINCT") {
			p.fail("COUNT DISTINCT is not supported")
			return
		}

		count.column = p.name()
	}

	p.expect(')')

	if p.word("AS") {
		count.alias = p.name()
	}

	p.statement.count = count
}

func (p *Plan) prepareCount(s *Statement, columns map[string]schema.Column) error {
	if len(s.orders) != 0 {
		return fmt.Errorf("ORDER BY with COUNT is not supported: %w", ErrUnsupported)
	}

	p.count = &countPlan{}
	name := "COUNT(*)"

	if !s.count.all {
		column, found := columns[s.count.column]
		if !found {
			return ErrColumn
		}

		p.count.columnID = column.ID
		name = "COUNT(" + column.Name + ")"
	}

	if s.count.alias != "" {
		name = s.count.alias
	}

	p.columns = []schema.Column{{Name: name, Type: record.TypeInt64}}
	return nil
}

func (p *Plan) Aggregate() string {
	if p == nil || p.count == nil {
		return ""
	}

	return "count"
}

func (p *Plan) countResult(result Result, rows []schema.Row) (Result, error) {
	var count int64

	for _, row := range rows {
		if p.count.columnID != 0 {
			value, exists := row[p.count.columnID]
			if !exists {
				return Result{}, database.ErrInvalidData
			}

			if value.IsNull() {
				continue
			}
		}

		count++
	}

	if p.limit > 0 && p.offset == 0 {
		result.Rows = [][]record.Value{{record.NewInt64(count)}}
	}

	return result, nil
}
