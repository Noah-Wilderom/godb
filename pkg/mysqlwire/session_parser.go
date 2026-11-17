package mysqlwire

import (
	"strings"
	"unicode/utf8"

	"github.com/Noah-Wilderom/godb/pkg/sql"
)

type sessionToken struct {
	kind byte
	text string
}

type sessionParser struct {
	tokens []sessionToken
	pos    int
}

type sessionExpression struct {
	name, alias       string
	function, literal bool
}

type sessionAssignment struct {
	name, value string
	null        bool
	parameter   bool
}

type sessionStatement struct {
	kind        string
	expressions []sessionExpression
	assignments []sessionAssignment
}

func sessionSyntax() error { return &Error{1064, "42000", "Invalid session statement syntax"} }
func sessionUnsupported() error {
	return &Error{1235, "42000", "Session feature is not supported by GoDB"}
}

func sessionTokens(query string) ([]sessionToken, error) {
	if len(query) > sql.MaxStatementBytes || !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return nil, sessionSyntax()
	}

	var tokens []sessionToken

	for i := 0; i < len(query); {
		c := query[i]
		if strings.ContainsRune(" \t\r\n", rune(c)) {
			i++
			continue
		}

		if len(tokens) >= sql.MaxTokens {
			return nil, sessionSyntax()
		}

		start := i
		i++

		t := sessionToken{kind: c, text: string(c)}
		switch {
		case sessionWord(c):
			for i < len(query) && (sessionWord(query[i]) || query[i] >= '0' && query[i] <= '9') {
				i++
			}

			t.kind, t.text = 'w', query[start:i]
		case c >= '0' && c <= '9':
			for i < len(query) && query[i] >= '0' && query[i] <= '9' {
				i++
			}

			t.kind, t.text = 'n', query[start:i]
		case c == '\'' || c == '"' || c == '`':
			var value strings.Builder

			closed := false

			for i < len(query) {
				b := query[i]
				i++

				if b == '\\' {
					return nil, sessionSyntax()
				}

				if b == c {
					if i < len(query) && query[i] == c {
						i++
						value.WriteByte(c)
						continue
					}

					closed = true
					break
				}

				value.WriteByte(b)
			}

			if !closed {
				return nil, sessionSyntax()
			}

			t.kind, t.text = 's', value.String()
			if c == '`' {
				t.kind = 'i'
			}
		case strings.ContainsRune("@.,()=;?*<>!+-", rune(c)):
		default:
			return nil, sessionSyntax()
		}

		tokens = append(tokens, t)
	}

	return append(tokens, sessionToken{}), nil
}

func sessionWord(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
func (p *sessionParser) take(kind byte) bool {
	if p.tokens[p.pos].kind != kind {
		return false
	}

	p.pos++
	return true
}

func (p *sessionParser) word(word string) bool {
	t := p.tokens[p.pos]
	if t.kind != 'w' || !strings.EqualFold(t.text, word) {
		return false
	}

	p.pos++
	return true
}

func (p *sessionParser) name() (string, error) {
	t := p.tokens[p.pos]
	if (t.kind != 'w' && t.kind != 'i') || len(t.text) == 0 || len(t.text) > sql.MaxIdentifierBytes {
		return "", sessionSyntax()
	}

	p.pos++
	return t.text, nil
}

func (p *sessionParser) variable() (string, error) {
	name, err := p.name()
	if err != nil {
		return "", err
	}

	if p.take('.') {
		if !strings.EqualFold(name, "session") && !strings.EqualFold(name, "local") {
			return "", sessionUnsupported()
		}

		name, err = p.name()
	}

	return strings.ToLower(name), err
}

func parseSession(query string) (*sessionStatement, bool, error) {
	trimmed := strings.TrimLeft(query, " \t\r\n")

	end := 0
	for end < len(trimmed) && sessionWord(trimmed[end]) {
		end++
	}

	first := trimmed[:end]

	local := strings.EqualFold(first, "set") || strings.EqualFold(first, "use") || strings.EqualFold(first, "show") || strings.EqualFold(first, "select") && localSessionSelect(trimmed[end:])
	if !local {
		return nil, false, nil
	}

	tokens, err := sessionTokens(query)
	if err != nil {
		return nil, local, err
	}

	p := sessionParser{tokens: tokens, pos: 1}
	first = strings.ToLower(first)

	s := &sessionStatement{kind: first}
	switch first {
	case "set":
		err = p.parseSet(s)
	case "select":
		err = p.parseSelect(s)
	case "use":
		var name string

		name, err = p.name()
		s.assignments = []sessionAssignment{{name: "database", value: name}}
	case "show":
		p.word("session")

		if !p.word("variables") {
			err = sessionUnsupported()
		} else if p.word("like") {
			if p.tokens[p.pos].kind != 's' || len(p.tokens[p.pos].text) > 64 {
				return nil, true, sessionSyntax()
			}

			s.expressions = []sessionExpression{{name: strings.ToLower(p.tokens[p.pos].text)}}
			p.pos++
		}
	}

	if err != nil {
		return nil, true, err
	}

	p.take(';')

	if p.tokens[p.pos].kind != 0 {
		return nil, true, sessionSyntax()
	}

	return s, true, nil
}

func (p *sessionParser) parseSet(s *sessionStatement) error {
	if p.word("names") {
		t := p.tokens[p.pos]
		if t.kind != 'w' && t.kind != 's' {
			return sessionSyntax()
		}

		p.pos++

		s.assignments = []sessionAssignment{{name: "character_set_client", value: t.text}, {name: "character_set_connection", value: t.text}, {name: "character_set_results", value: t.text}}
		if p.word("collate") {
			t = p.tokens[p.pos]
			if t.kind != 'w' && t.kind != 's' {
				return sessionSyntax()
			}

			p.pos++
			s.assignments = append(s.assignments, sessionAssignment{name: "collation_connection", value: t.text})
		}

		return nil
	}

	if !p.word("session") {
		p.word("local")
	}

	for {
		if len(s.assignments) >= sql.MaxColumns {
			return sessionSyntax()
		}

		if p.take('@') && !p.take('@') {
			return sessionUnsupported()
		}

		name, err := p.variable()
		if err != nil {
			return err
		}

		if !p.take('=') {
			return sessionSyntax()
		}

		t := p.tokens[p.pos]
		if t.kind != 'w' && t.kind != 's' && t.kind != 'n' && t.kind != '?' {
			return sessionSyntax()
		}

		p.pos++
		s.assignments = append(s.assignments, sessionAssignment{name: name, value: t.text, null: t.kind == 'w' && strings.EqualFold(t.text, "null"), parameter: t.kind == '?'})

		if !p.take(',') {
			return nil
		}
	}
}

func (p *sessionParser) parseSelect(s *sessionStatement) error {
	for {
		if len(s.expressions) >= sql.MaxColumns {
			return sessionSyntax()
		}

		var expression sessionExpression

		if p.take('@') {
			if !p.take('@') {
				return sessionUnsupported()
			}

			start := p.pos

			name, err := p.variable()
			if err != nil {
				return err
			}

			expression.name, expression.alias = name, "@@"
			for _, token := range p.tokens[start:p.pos] {
				expression.alias += token.text
			}
		} else if p.tokens[p.pos].kind == 'n' && p.tokens[p.pos].text == "1" {
			p.pos++
			expression.name, expression.alias, expression.literal = "1", "1", true
		} else {
			name, err := p.name()
			if err != nil {
				return err
			}

			switch strings.ToLower(name) {
			case "version", "database", "connection_id":
			default:
				return sessionUnsupported()
			}

			if !p.take('(') || !p.take(')') {
				return sessionSyntax()
			}

			expression.name, expression.alias, expression.function = strings.ToLower(name), name+"()", true
		}

		if p.word("as") {
			name, err := p.name()
			if err != nil {
				return err
			}

			expression.alias = name
		}

		s.expressions = append(s.expressions, expression)

		if !p.take(',') {
			return nil
		}
	}
}

func localSessionSelect(query string) bool {
	query = strings.TrimLeft(query, " \t\r\n")
	if len(query) == 0 {
		return false
	}

	if query[0] == '@' || query[0] >= '0' && query[0] <= '9' {
		return true
	}

	end := 0
	for end < len(query) && sessionWord(query[end]) {
		end++
	}

	name := query[:end]
	return (strings.EqualFold(name, "version") || strings.EqualFold(name, "database") || strings.EqualFold(name, "connection_id")) && strings.HasPrefix(strings.TrimLeft(query[end:], " \t\r\n"), "(")
}
