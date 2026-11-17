package sql

import (
	"fmt"
	"strings"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type Kind string

const (
	CreateTable Kind = "create_table"
	Insert      Kind = "insert"
	Select      Kind = "select"
	Explain     Kind = "explain"
	Update      Kind = "update"
	Delete      Kind = "delete"
)

type expression struct {
	literal   token
	parameter int
}

type columnDefinition struct {
	column       schema.Column
	explicitNull bool
}

type indexDefinition struct {
	name    string
	columns []string
	unique  bool
}

type filterPredicate struct {
	operation predicateOperator
	column    string
	value     expression
}

type ordering struct {
	column     string
	descending bool
}

type Statement struct {
	count             *countExpression
	indexes           []indexDefinition
	kind              Kind
	table, primaryKey string
	definitions       []columnDefinition
	columns           []string
	values            []expression
	all               bool
	predicates        []filterPredicate
	orders            []ordering
	limit, offset     *expression
	parameters        int
}

func (s *Statement) Kind() Kind { return s.kind }

type parser struct {
	tokens    []token
	index     int
	err       error
	statement Statement
}

func Parse(query string) (*Statement, error) {
	tokens, err := lex(query)
	if err != nil {
		return nil, err
	}

	p := &parser{tokens: tokens}
	switch {
	case p.word("CREATE"):
		p.statement.kind = CreateTable
		p.create()
	case p.word("INSERT"):
		p.statement.kind = Insert
		p.insert()
	case p.word("UPDATE"):
		p.statement.kind = Update
		p.update()
	case p.word("DELETE"):
		p.statement.kind = Delete
		p.deleteRow()
	case p.word("EXPLAIN"):
		p.statement.kind = Explain
		p.keyword("SELECT")

		if p.err == nil {
			p.selectRow()
		}
	case p.word("SELECT"):
		p.statement.kind = Select
		p.selectRow()
	default:
		p.fail("expected CREATE TABLE, INSERT, UPDATE, DELETE, SELECT, or EXPLAIN SELECT")
	}

	p.take(';')

	if p.peek().kind != 0 {
		p.fail("unexpected trailing syntax or multiple statements")
	}

	if p.err != nil {
		return nil, p.err
	}

	return &p.statement, nil
}

func (p *parser) peek() token { return p.tokens[p.index] }
func (p *parser) advance() token {
	t := p.peek()
	if t.kind != 0 {
		p.index++
	}

	return t
}

func (p *parser) fail(message string) {
	if p.err == nil {
		p.err = fmt.Errorf("byte %d: %s: %w", p.peek().position, message, ErrSyntax)
	}
}

func (p *parser) take(kind byte) bool {
	if p.peek().kind != kind {
		return false
	}

	p.advance()
	return true
}

func (p *parser) expect(kind byte) {
	if !p.take(kind) {
		p.fail(fmt.Sprintf("expected %q", kind))
	}
}

func (p *parser) word(word string) bool {
	t := p.peek()
	if t.kind != tokenWord || !strings.EqualFold(t.text, word) {
		return false
	}

	p.advance()
	return true
}

func (p *parser) keyword(word string) {
	if !p.word(word) {
		p.fail("expected " + word)
	}
}

func (p *parser) name() string {
	t := p.peek()
	if t.kind != tokenWord && t.kind != tokenIdentifier {
		p.fail("expected identifier")
		return ""
	}

	if len(t.text) == 0 || len(t.text) > MaxIdentifierBytes {
		p.fail("identifier must contain 1..64 bytes")
		return ""
	}

	if t.kind == tokenWord {
		switch strings.ToUpper(t.text) {
		case "EXPLAIN", "CREATE", "TABLE", "INSERT", "INTO", "VALUES", "SELECT", "FROM", "WHERE", "PRIMARY", "KEY", "NOT", "NULL", "TRUE", "FALSE", "UNSIGNED", "UPDATE", "DELETE", "DROP", "DEFAULT", "ORDER", "LIMIT", "AND", "OR", "UNIQUE", "INDEX", "CONSTRAINT", "FOREIGN", "CHECK", "BY", "ASC", "DESC", "OFFSET", "SET":
			p.fail("quote reserved identifiers with backticks")
			return ""
		}
	}

	p.advance()
	return t.text
}

func (p *parser) create() {
	p.keyword("TABLE")
	p.statement.table = p.name()
	p.expect('(')

	for p.err == nil {
		if p.word("PRIMARY") {
			p.keyword("KEY")
			p.expect('(')
			p.primary(p.name())
			p.expect(')')
		} else if p.word("UNIQUE") {
			p.indexDefinition(true, "")
		} else if p.word("INDEX") || p.word("KEY") {
			p.indexDefinition(false, "")
		} else if p.word("CONSTRAINT") {
			name := p.name()
			p.keyword("UNIQUE")
			p.indexDefinition(true, name)
		} else {
			if len(p.statement.definitions) >= MaxColumns {
				p.err = ErrLimit
				break
			}

			definition := columnDefinition{column: schema.Column{Name: p.name(), Nullable: true}}
			switch {
			case p.word("BIGINT"):
				definition.column.Type = record.TypeInt64
				if p.word("UNSIGNED") {
					definition.column.Type = record.TypeUint64
				}
			case p.word("TEXT"):
				definition.column.Type = record.TypeText
			case p.word("BOOLEAN") || p.word("BOOL"):
				definition.column.Type = record.TypeBool
			default:
				p.fail("supported types are BIGINT [UNSIGNED], TEXT, BOOLEAN")
			}

			seenNull, seenPrimary, seenUnique := false, false, false

			for p.err == nil {
				switch {
				case p.word("NOT"):
					if seenNull {
						p.fail("repeated nullability")
					}

					p.keyword("NULL")
					seenNull, definition.column.Nullable = true, false
				case p.word("NULL"):
					if seenNull {
						p.fail("repeated nullability")
					}

					seenNull, definition.explicitNull = true, true
				case p.word("UNIQUE"):
					if seenUnique {
						p.fail("repeated unique declaration")
					}

					p.word("KEY")
					p.addIndex(indexDefinition{columns: []string{definition.column.Name}, unique: true})
					seenUnique = true
				case p.word("PRIMARY"):
					if seenPrimary {
						p.fail("repeated primary key")
					}

					p.keyword("KEY")
					p.primary(definition.column.Name)
					seenPrimary = true
				default:
					goto modifiersDone
				}
			}

		modifiersDone:
			p.statement.definitions = append(p.statement.definitions, definition)
		}

		if !p.take(',') {
			break
		}
	}

	p.expect(')')

	if len(p.statement.definitions) == 0 || p.statement.primaryKey == "" {
		p.fail("a table requires columns and one primary key")
	}
}

func (p *parser) primary(name string) {
	if p.statement.primaryKey != "" {
		p.fail("only one single-column primary key is supported")
	}

	p.statement.primaryKey = name
}

func (p *parser) names() []string {
	var names []string

	for p.err == nil {
		if len(names) >= MaxColumns {
			p.err = ErrLimit
			break
		}

		names = append(names, p.name())
		if !p.take(',') {
			break
		}
	}

	return names
}

func (p *parser) insert() {
	p.keyword("INTO")
	p.statement.table = p.name()
	p.expect('(')
	p.statement.columns = p.names()
	p.expect(')')
	p.keyword("VALUES")
	p.expect('(')

	for p.err == nil {
		if len(p.statement.values) >= MaxColumns {
			p.err = ErrLimit
			break
		}

		p.statement.values = append(p.statement.values, p.value())
		if !p.take(',') {
			break
		}
	}

	p.expect(')')

	if len(p.statement.columns) != len(p.statement.values) {
		p.fail("column and value counts differ")
	}
}

func (p *parser) selectRow() {
	if p.peek().kind == tokenWord && strings.EqualFold(p.peek().text, "COUNT") && p.index+1 < len(p.tokens) && p.tokens[p.index+1].kind == '(' {
		p.countProjection()
	} else {
		p.statement.all = p.take('*')
	}

	if !p.statement.all && p.statement.count == nil {
		p.statement.columns = p.names()
	}

	p.keyword("FROM")

	p.statement.table = p.name()
	if !p.word("WHERE") {
		p.selectWindow()
		return
	}

	for p.err == nil {
		if len(p.statement.predicates) == MaxPredicates {
			p.err = fmt.Errorf("too many WHERE predicates: %w", ErrLimit)
			return
		}

		predicate := p.selectPredicate()

		p.statement.predicates = append(p.statement.predicates, predicate)
		if !p.word("AND") {
			break
		}
	}

	p.selectWindow()
}

func (p *parser) value() expression {
	value := expression{parameter: -1}
	if p.take('?') {
		value.parameter = p.statement.parameters
		p.statement.parameters++
		return value
	}

	t := p.peek()
	if t.kind == tokenNumber || t.kind == tokenString {
		value.literal = p.advance()
		return value
	}

	if p.word("NULL") || p.word("TRUE") || p.word("FALSE") {
		value.literal = t
		return value
	}

	p.fail("expected literal or parameter")
	return value
}
