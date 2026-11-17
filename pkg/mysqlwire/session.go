package mysqlwire

import (
	"context"
	"strings"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

const (
	serverVersion = "8.0.0-GoDB"
)

var sessionVariableNames = []string{
	"autocommit", "character_set_client", "character_set_connection", "character_set_results",
	"collation_connection", "lower_case_table_names", "max_allowed_packet", "version", "version_comment",
}

type connectionState struct {
	id          uint32
	resultsNull bool
}

func (s *statementSession) query(ctx context.Context, query string, args ...record.Value) (sql.Result, error) {
	information, informationMatched, informationErr := parseInformation(query)
	if informationMatched {
		if informationErr != nil {
			s.endpoint.informationCommands.Add(1)
			s.endpoint.informationErrors.Add(1)
			return sql.Result{}, informationErr
		}

		result, _, err := s.information(ctx, information, false, args)
		return result, err
	}

	discovery, matched, discoveryErr := parseDiscovery(query)
	if matched {
		if discoveryErr != nil {
			s.endpoint.discoveryCommands.Add(1)
			s.endpoint.discoveryErrors.Add(1)
			return sql.Result{}, discoveryErr
		}

		return s.discovery(ctx, discovery, false, args)
	}

	statement, local, err := parseSession(query)
	if !local {
		return s.endpoint.query.Query(ctx, query, args...)
	}

	s.endpoint.sessionCommands.Add(1)

	var result sql.Result

	if err == nil {
		result, err = s.runSession(statement, false, args)
	}

	if err != nil {
		s.endpoint.sessionErrors.Add(1)
	}

	return result, err
}

func (s *statementSession) describe(ctx context.Context, query string) (sql.Description, error) {
	information, informationMatched, informationErr := parseInformation(query)
	if informationMatched {
		if informationErr != nil {
			return sql.Description{}, informationErr
		}

		_, description, err := s.information(ctx, information, true, nil)
		return description, err
	}

	discovery, matched, discoveryErr := parseDiscovery(query)
	if matched {
		if discoveryErr != nil {
			return sql.Description{}, discoveryErr
		}

		result, err := s.discovery(ctx, discovery, true, nil)
		if err != nil {
			return sql.Description{}, err
		}

		description := sql.Description{Columns: result.Columns}
		if discovery.parameter {
			description.Parameters = []schema.Column{{Name: "pattern", Type: record.TypeText}}
		}

		return description, nil
	}

	statement, local, err := parseSession(query)
	if !local {
		if s.endpoint.query.Describe == nil {
			return sql.Description{}, sessionUnsupported()
		}

		return s.endpoint.query.Describe(ctx, query)
	}

	if err != nil {
		return sql.Description{}, err
	}

	result, err := s.runSession(statement, true, nil)
	if err != nil {
		return sql.Description{}, err
	}

	description := sql.Description{Columns: result.Columns}

	for _, assignment := range statement.assignments {
		if assignment.parameter {
			typ := record.TypeText
			if assignment.name == "autocommit" {
				typ = record.TypeInt64
			}

			description.Parameters = append(description.Parameters, schema.Column{Name: assignment.name, Type: typ, Nullable: assignment.name == "character_set_results"})
		}
	}

	return description, nil
}

func (s *statementSession) variable(name string) (record.Value, record.Type, bool, error) {
	switch name {
	case "version":
		return sessionText(serverVersion, false)
	case "version_comment":
		return sessionText("GoDB", false)
	case "autocommit":
		return record.NewInt64(1), record.TypeInt64, false, nil
	case "lower_case_table_names":
		return record.NewInt64(0), record.TypeInt64, false, nil
	case "max_allowed_packet":
		return record.NewUint64(maxPacket), record.TypeUint64, false, nil
	case "character_set_client", "character_set_connection":
		return sessionText("utf8mb4", false)
	case "character_set_results":
		if s.state.resultsNull {
			return record.NewNull(), record.TypeText, true, nil
		}

		return sessionText("utf8mb4", true)
	case "collation_connection":
		return sessionText("utf8mb4_bin", false)
	default:
		return record.Value{}, record.TypeNull, false, &Error{1193, "HY000", "Unknown or unsupported session variable"}
	}
}

func (s *statementSession) runSession(statement *sessionStatement, describe bool, args []record.Value) (sql.Result, error) {
	result := sql.Result{}
	candidate := s.state
	argument := 0

	for _, assignment := range statement.assignments {
		if assignment.parameter {
			if describe {
				if err := writableSessionVariable(assignment.name); err != nil {
					return result, err
				}

				continue
			}

			if argument >= len(args) {
				return result, &Error{1210, "HY000", "Incorrect session parameter count"}
			}

			value := args[argument]
			argument++

			assignment.null = value.IsNull()
			if !assignment.null {
				var err error

				assignment.value, err = textValue(value)
				if err != nil {
					return result, err
				}
			}
		}

		if statement.kind == "use" {
			if assignment.value != s.endpoint.config.Database {
				return result, &Error{1049, "42000", "Unknown database"}
			}
		} else if err := candidate.assign(assignment); err != nil {
			return result, err
		}
	}

	if argument != len(args) {
		return result, &Error{1210, "HY000", "Incorrect session parameter count"}
	}

	if statement.kind == "show" {
		result.Columns = []schema.Column{{Name: "Variable_name", Type: record.TypeText}, {Name: "Value", Type: record.TypeText}}

		if !describe {
			for _, name := range sessionVariableNames {
				if len(statement.expressions) > 0 && !sessionLike(name, statement.expressions[0].name) {
					continue
				}

				value, _, _, err := s.variable(name)
				if err != nil {
					return result, err
				}

				text := "NULL"
				if !value.IsNull() {
					text, err = textValue(value)
					if err != nil {
						return result, err
					}
				}

				nameValue, err := record.NewText(name)
				if err != nil {
					return result, err
				}

				textValue, err := record.NewText(text)
				if err != nil {
					return result, err
				}

				result.Rows = append(result.Rows, []record.Value{nameValue, textValue})
			}
		}
	} else if statement.kind == "select" {
		var row []record.Value

		for _, expression := range statement.expressions {
			value, typ, nullable, err := s.expression(expression)
			if err != nil {
				return result, err
			}

			result.Columns = append(result.Columns, schema.Column{Name: expression.alias, Type: typ, Nullable: nullable})
			row = append(row, value)
		}

		result.Rows = [][]record.Value{row}
	}

	if !describe {
		s.state = candidate
	}

	return result, nil
}

func writableSessionVariable(name string) error {
	switch name {
	case "autocommit", "character_set_client", "character_set_connection", "character_set_results", "collation_connection":
		return nil
	default:
		return sessionUnsupported()
	}
}

func (s *connectionState) assign(assignment sessionAssignment) error {
	if err := writableSessionVariable(assignment.name); err != nil {
		return err
	}

	value := strings.ToLower(assignment.value)
	valid := false

	switch assignment.name {
	case "autocommit":
		valid = value == "1" || value == "on" || value == "true"
	case "character_set_client", "character_set_connection":
		valid = value == "utf8mb4"
	case "character_set_results":
		valid = value == "utf8mb4" || assignment.null
		if valid {
			s.resultsNull = assignment.null
		}
	case "collation_connection":
		valid = value == "utf8mb4_bin"
	}

	if !valid || assignment.null && assignment.name != "character_set_results" {
		return &Error{1231, "42000", "Unsupported value for session variable"}
	}

	return nil
}

func sessionLike(name, pattern string) bool {
	matched := make([]bool, len(name)+1)

	matched[0] = true
	for _, c := range pattern {
		previous := matched[0]

		matched[0] = matched[0] && c == '%'
		for i := 1; i <= len(name); i++ {
			old := matched[i]
			if c == '%' {
				matched[i] = matched[i] || matched[i-1]
			} else {
				matched[i] = previous && (c == '_' || c == rune(name[i-1]))
			}

			previous = old
		}
	}

	return matched[len(name)]
}

func sessionText(text string, nullable bool) (record.Value, record.Type, bool, error) {
	value, err := record.NewText(text)
	return value, record.TypeText, nullable, err
}

func (s *statementSession) expression(expression sessionExpression) (record.Value, record.Type, bool, error) {
	if expression.literal {
		return record.NewInt64(1), record.TypeInt64, false, nil
	}

	if expression.function {
		switch expression.name {
		case "database":
			return sessionText(s.endpoint.config.Database, false)
		case "connection_id":
			return record.NewUint64(uint64(s.state.id)), record.TypeUint64, false, nil
		}
	}

	return s.variable(expression.name)
}
