package sql

import (
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type predicateOperator uint8

const (
	predicateEqual predicateOperator = iota
	predicateIsNull
	predicateIsNotNull
	predicateLess
	predicateLessEqual
	predicateGreater
	predicateGreaterEqual
)

func (p *parser) selectPredicate() filterPredicate {
	predicate := filterPredicate{column: p.name()}
	if p.word("IS") {
		predicate.operation = predicateIsNull
		if p.word("NOT") {
			predicate.operation = predicateIsNotNull
		}

		p.keyword("NULL")
	} else {
		switch {
		case p.take('<'):
			predicate.operation = predicateLess
		case p.take(tokenLessEqual):
			predicate.operation = predicateLessEqual
		case p.take('>'):
			predicate.operation = predicateGreater
		case p.take(tokenGreaterEqual):
			predicate.operation = predicateGreaterEqual
		default:
			p.expect('=')
		}

		predicate.value = p.value()
	}

	return predicate
}

func uniqueLookup(index schema.Index, prefix int, values map[uint32]record.Value) bool {
	if !index.Unique || prefix != len(index.ColumnIDs) {
		return false
	}

	for _, id := range index.ColumnIDs {
		if values[id].IsNull() {
			return false
		}
	}

	return true
}
