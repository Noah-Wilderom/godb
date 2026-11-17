package sql

import (
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func (p *parser) primaryPredicate() {
	p.keyword("WHERE")
	predicate := filterPredicate{column: p.name()}
	p.expect('=')
	predicate.value = p.value()
	p.statement.predicates = append(p.statement.predicates, predicate)
}

func (p *Plan) resolvePrimaryKey(s *Statement, columns map[string]schema.Column, resolve func(expression, schema.Column) (record.Value, error)) (record.Value, error) {
	predicate := s.predicates[0]

	column, exists := columns[predicate.column]
	if !exists {
		return record.Value{}, ErrColumn
	}

	if column.ID != p.table.PrimaryKeyColumnID {
		return record.Value{}, fmt.Errorf("%s requires primary-key equality: %w", p.kind, ErrUnsupported)
	}

	column.Nullable = true
	return resolve(predicate.value, column)
}

func (p *Plan) ApplyResult(outcome database.CommandResult) (Result, error) {
	if p == nil || len(p.command) == 0 {
		return Result{}, ErrUnsupported
	}

	if outcome.Code != database.CommandOK {
		return Result{}, &CommandError{Code: outcome.Code}
	}

	switch p.kind {
	case Insert:
		return Result{AffectedRows: 1, MatchedRows: 1}, nil
	case Update, Delete:
		return Result{AffectedRows: outcome.AffectedRows, MatchedRows: outcome.MatchedRows}, nil
	default:
		return Result{}, nil
	}
}
