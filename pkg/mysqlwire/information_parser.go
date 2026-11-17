package mysqlwire

import (
	"strconv"
	"strings"
)

type (
	informationReference  struct{ qualifier, name string }
	informationProjection struct {
		reference informationReference
		alias     string
	}
	informationPredicate struct {
		reference                 informationReference
		operation, value          string
		parameter, null, database bool
		number                    bool
	}
	informationOrder struct {
		reference  informationReference
		descending bool
	}
	informationStatement struct {
		view, alias   string
		projections   []informationProjection
		predicates    []informationPredicate
		orders        []informationOrder
		star, count   bool
		countAlias    string
		limit, offset int
	}
)

func parseInformation(query string) (*informationStatement, bool, error) {
	trimmed := strings.TrimLeft(query, " \t\r\n")
	if len(trimmed) < 6 || !strings.EqualFold(trimmed[:6], "select") || !containsInformationSchema(trimmed) {
		return nil, false, nil
	}

	tokens, err := sessionTokens(query)
	if err != nil {
		return nil, false, err
	}

	matched := false

	for i := 0; i+2 < len(tokens); i++ {
		if tokens[i].kind == 'w' && strings.EqualFold(tokens[i].text, "from") && (tokens[i+1].kind == 'w' || tokens[i+1].kind == 'i') && strings.EqualFold(tokens[i+1].text, "information_schema") && tokens[i+2].kind == '.' {
			matched = true
			break
		}
	}

	if !matched {
		return nil, false, nil
	}

	p := sessionParser{tokens: tokens}
	if !p.word("select") {
		return nil, true, sessionSyntax()
	}

	s := &informationStatement{limit: maxResultRows}
	if p.take('*') {
		s.star = true
	} else if p.word("count") {
		if !p.take('(') || !p.take('*') || !p.take(')') {
			return nil, true, sessionSyntax()
		}

		s.count, s.countAlias = true, "COUNT(*)"
		if p.word("as") {
			s.countAlias, err = p.name()
			if err != nil {
				return nil, true, err
			}
		}
	} else {
		for {
			if len(s.projections) >= 64 {
				return nil, true, resultLimit()
			}

			reference, err := p.informationReference()
			if err != nil {
				return nil, true, err
			}

			projection := informationProjection{reference: reference, alias: reference.name}
			if p.word("as") {
				projection.alias, err = p.name()
				if err != nil {
					return nil, true, err
				}
			}

			s.projections = append(s.projections, projection)

			if !p.take(',') {
				break
			}
		}
	}

	if !p.word("from") {
		return nil, true, sessionSyntax()
	}

	namespace, err := p.name()
	if err != nil || !strings.EqualFold(namespace, "information_schema") || !p.take('.') {
		return nil, true, sessionSyntax()
	}

	s.view, err = p.name()
	if err != nil {
		return nil, true, err
	}

	s.view = strings.ToUpper(s.view)
	if p.word("as") {
		s.alias, err = p.name()
		if err != nil {
			return nil, true, err
		}
	} else if t := p.tokens[p.pos]; (t.kind == 'w' || t.kind == 'i') && !strings.EqualFold(t.text, "where") && !strings.EqualFold(t.text, "order") && !strings.EqualFold(t.text, "limit") {
		s.alias, err = p.name()
		if err != nil {
			return nil, true, err
		}
	}

	if p.word("where") {
		for {
			if len(s.predicates) >= 64 {
				return nil, true, resultLimit()
			}

			reference, err := p.informationReference()
			if err != nil {
				return nil, true, err
			}

			predicate := informationPredicate{reference: reference}

			switch {
			case p.take('='):
				predicate.operation = "equal"
			case p.word("like"):
				predicate.operation = "like"
			case p.word("is"):
				predicate.operation = "null"
				if p.word("not") {
					predicate.operation = "not_null"
				}

				if !p.word("null") {
					return nil, true, sessionSyntax()
				}
			default:
				return nil, true, sessionSyntax()
			}

			if predicate.operation == "equal" || predicate.operation == "like" {
				t := p.tokens[p.pos]
				switch {
				case p.take('?'):
					predicate.parameter = true
				case p.word("null"):
					predicate.null = true
				case p.word("database"):
					if !p.take('(') || !p.take(')') {
						return nil, true, sessionSyntax()
					}

					predicate.database = true
				case t.kind == 's' || t.kind == 'n':
					predicate.value, predicate.number = t.text, t.kind == 'n'
					p.pos++
				default:
					return nil, true, sessionSyntax()
				}
			}

			s.predicates = append(s.predicates, predicate)

			if !p.word("and") {
				break
			}
		}
	}

	if p.word("order") {
		if !p.word("by") || s.count {
			return nil, true, sessionSyntax()
		}

		for {
			if len(s.orders) >= 64 {
				return nil, true, resultLimit()
			}

			reference, err := p.informationReference()
			if err != nil {
				return nil, true, err
			}

			descending := p.word("desc")
			if !descending {
				p.word("asc")
			}

			s.orders = append(s.orders, informationOrder{reference: reference, descending: descending})

			if !p.take(',') {
				break
			}
		}
	}

	if p.word("limit") {
		s.limit, err = p.informationLimit()
		if err != nil {
			return nil, true, err
		}

		if p.word("offset") {
			s.offset, err = p.informationLimit()
			if err != nil {
				return nil, true, err
			}
		}
	}

	p.take(';')

	if p.tokens[p.pos].kind != 0 {
		return nil, true, sessionSyntax()
	}

	return s, true, nil
}

func (p *sessionParser) informationReference() (informationReference, error) {
	name, err := p.name()
	if err != nil {
		return informationReference{}, err
	}

	ref := informationReference{name: name}
	if p.take('.') {
		ref.qualifier = name
		ref.name, err = p.name()
	}

	return ref, err
}

func (p *sessionParser) informationLimit() (int, error) {
	t := p.tokens[p.pos]
	if t.kind != 'n' {
		return 0, sessionSyntax()
	}

	p.pos++

	value, err := strconv.ParseUint(t.text, 10, 16)
	if err != nil || value > maxResultRows {
		return 0, resultLimit()
	}

	return int(value), nil
}

func containsInformationSchema(query string) bool {
	const name = "information_schema"

	for i := 0; i+len(name) <= len(query); i++ {
		if (query[i] == 'i' || query[i] == 'I') && strings.EqualFold(query[i:i+len(name)], name) {
			return true
		}
	}

	return false
}
