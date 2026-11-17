package sql

import (
	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func (p *parser) deleteRow() {
	p.keyword("FROM")
	p.statement.table = p.name()
	p.primaryPredicate()
}

func (p *Plan) prepareDelete(s *Statement, columns map[string]schema.Column, resolve func(expression, schema.Column) (record.Value, error), describe bool) (*Plan, error) {
	key, err := p.resolvePrimaryKey(s, columns, resolve)
	if err != nil {
		return nil, err
	}

	if describe {
		return p, nil
	}

	p.command, err = database.EncodeCommand(database.Command{Operations: []database.CommandOperation{{Kind: database.CommandDeleteRowCounted, Table: &p.table, PrimaryKey: &key}}})
	if err != nil {
		return nil, err
	}

	return p, nil
}
