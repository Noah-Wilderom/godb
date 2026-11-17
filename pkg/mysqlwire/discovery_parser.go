package mysqlwire

import "strings"

type discoveryStatement struct {
	kind, database, table      string
	full                       bool
	pattern                    string
	filtered, parameter, exact bool
}

func parseDiscovery(query string) (*discoveryStatement, bool, error) {
	trimmed := strings.TrimLeft(query, " \t\r\n")

	end := 0
	for end < len(trimmed) && sessionWord(trimmed[end]) {
		end++
	}

	first := trimmed[:end]

	show, describe := strings.EqualFold(first, "show"), strings.EqualFold(first, "describe") || strings.EqualFold(first, "desc")
	if !show && !describe {
		return nil, false, nil
	}

	tokens, err := sessionTokens(query)
	if err != nil {
		return nil, describe, err
	}

	p := sessionParser{tokens: tokens, pos: 1}

	s := &discoveryStatement{}
	if describe {
		s.kind = "columns"
	} else {
		s.full = p.word("full")
		switch {
		case p.word("databases"), p.word("schemas"):
			s.kind = "databases"
		case p.word("tables"):
			s.kind = "tables"
		case p.word("columns"), p.word("fields"):
			s.kind = "columns"
		case p.word("index"), p.word("indexes"), p.word("keys"):
			s.kind = "indexes"
		default:
			return nil, false, nil
		}
	}

	if s.full && s.kind != "tables" && s.kind != "columns" {
		return nil, true, sessionSyntax()
	}

	if s.kind == "columns" || s.kind == "indexes" {
		if !describe && !p.word("from") && !p.word("in") {
			return nil, true, sessionSyntax()
		}

		s.table, err = p.name()
		if err != nil || s.table == "" {
			return nil, true, sessionSyntax()
		}

		if p.take('.') {
			s.database = s.table

			s.table, err = p.name()
			if err != nil || s.table == "" {
				return nil, true, sessionSyntax()
			}
		}
	}

	if s.kind != "databases" && !describe && (p.word("from") || p.word("in")) {
		name, err := p.name()
		if err != nil || name == "" || s.database != "" {
			return nil, true, sessionSyntax()
		}

		s.database = name
	}

	if s.kind != "indexes" {
		if describe {
			t := p.tokens[p.pos]
			if t.kind == 's' || t.kind == 'w' || t.kind == 'i' {
				p.pos++
				s.filtered, s.pattern, s.exact = true, t.text, t.kind != 's'
			}
		} else if p.word("like") {
			t := p.tokens[p.pos]
			if t.kind != 's' && t.kind != '?' {
				return nil, true, sessionSyntax()
			}

			p.pos++
			s.filtered, s.pattern, s.parameter = true, t.text, t.kind == '?'
		}
	}

	if len(s.pattern) > 64 {
		return nil, true, sessionSyntax()
	}

	p.take(';')

	if p.tokens[p.pos].kind != 0 {
		return nil, true, sessionSyntax()
	}

	return s, true, nil
}
