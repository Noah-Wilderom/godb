package sql

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxStatementBytes  = 64 << 10
	MaxTokens          = 4096
	MaxColumns         = 64
	MaxIdentifierBytes = 64

	tokenWord         byte = 'w'
	tokenIdentifier   byte = 'i'
	tokenString       byte = 's'
	tokenNumber       byte = 'n'
	tokenLessEqual    byte = 'L'
	tokenGreaterEqual byte = 'G'
)

type token struct {
	kind     byte
	text     string
	position int
}

func lex(query string) ([]token, error) {
	if len(query) > MaxStatementBytes {
		return nil, ErrLimit
	}

	if !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return nil, ErrSyntax
	}

	tokens := make([]token, 0)

	for i := 0; i < len(query); {
		c := query[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}

		if len(tokens) >= MaxTokens {
			return nil, ErrLimit
		}

		start := i
		i++

		t := token{kind: c, position: start}
		switch {
		case isLetter(c):
			for i < len(query) && (isLetter(query[i]) || isDigit(query[i])) {
				i++
			}

			t.kind, t.text = tokenWord, query[start:i]
		case isDigit(c) || ((c == '-' || c == '+') && i < len(query) && isDigit(query[i])):
			for i < len(query) && isDigit(query[i]) {
				i++
			}

			t.kind, t.text = tokenNumber, query[start:i]
		case c == '\'' || c == '`':
			var value strings.Builder

			closed := false

			for i < len(query) {
				b := query[i]

				i++
				if b == '\\' {
					return nil, fmt.Errorf("byte %d: backslash escapes are unsupported: %w", i-1, ErrSyntax)
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
				return nil, fmt.Errorf("byte %d: unterminated quote: %w", start, ErrSyntax)
			}

			t.kind, t.text = tokenString, value.String()
			if c == '`' {
				t.kind = tokenIdentifier
			}
		case c == '<' || c == '>':
			if i < len(query) && query[i] == '=' {
				i++

				t.kind = tokenLessEqual
				if c == '>' {
					t.kind = tokenGreaterEqual
				}
			}
		case strings.ContainsRune("(),*=;?", rune(c)):
		default:
			return nil, fmt.Errorf("byte %d: unexpected character: %w", start, ErrSyntax)
		}

		tokens = append(tokens, t)
	}

	return append(tokens, token{position: len(query)}), nil
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
