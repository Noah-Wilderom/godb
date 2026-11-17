package sql

import (
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func (p *parser) update() {
	p.statement.table = p.name()
	p.keyword("SET")

	for p.err == nil {
		if len(p.statement.columns) >= MaxColumns {
			p.err = ErrLimit
			return
		}

		p.statement.columns = append(p.statement.columns, p.name())
		p.expect('=')

		p.statement.values = append(p.statement.values, p.value())
		if !p.take(',') {
			break
		}
	}

	p.primaryPredicate()
}

func (p *Plan) prepareUpdate(s *Statement, columns map[string]schema.Column, resolve func(expression, schema.Column) (record.Value, error), describe bool) (*Plan, error) {
	changes := make(schema.Row, len(s.columns))
	for i, name := range s.columns {
		column, exists := columns[name]
		if !exists {
			return nil, ErrColumn
		}

		if column.ID == p.table.PrimaryKeyColumnID {
			return nil, fmt.Errorf("updating the primary key is unsupported: %w", ErrUnsupported)
		}

		if _, exists := changes[column.ID]; exists {
			return nil, ErrColumn
		}

		value, err := resolve(s.values[i], column)
		if err != nil {
			return nil, err
		}

		changes[column.ID] = value
	}

	key, err := p.resolvePrimaryKey(s, columns, resolve)
	if err != nil {
		return nil, err
	}

	if describe {
		return p, nil
	}

	p.command, err = database.EncodeCommand(database.Command{Operations: []database.CommandOperation{{Kind: database.CommandPatchRow, Table: &p.table, PrimaryKey: &key, Row: changes}}})
	if err != nil {
		return nil, err
	}

	return p, nil
}
