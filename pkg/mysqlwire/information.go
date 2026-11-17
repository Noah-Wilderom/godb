package mysqlwire

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

const (
	maxInformationScanRows    = 65536
	maxInformationFilterBytes = 1024
)

type (
	informationFilter struct {
		column    int
		operation string
		value     record.Value
	}
	informationSort struct {
		column     int
		descending bool
	}
	informationPlan struct {
		statement   *informationStatement
		columns     []schema.Column
		description sql.Description
		projections []int
		filters     []informationFilter
		orders      []informationSort
	}
)

func informationColumnError() error {
	return &Error{1054, "42S22", "Unknown or ambiguous INFORMATION_SCHEMA column"}
}

func informationBindingError() error {
	return &Error{1366, "HY000", "Invalid INFORMATION_SCHEMA filter value"}
}

func compileInformation(statement *informationStatement, database string, describe bool, args []record.Value) (*informationPlan, error) {
	p := &informationPlan{statement: statement, columns: informationColumns(statement.view)}
	if p.columns == nil {
		return nil, &Error{1109, "42S02", "Unknown INFORMATION_SCHEMA table"}
	}

	resolve := func(reference informationReference) (int, error) {
		qualifier := statement.view
		if statement.alias != "" {
			qualifier = statement.alias
		}

		if reference.qualifier != "" && !strings.EqualFold(reference.qualifier, qualifier) {
			return 0, informationColumnError()
		}

		for i, column := range p.columns {
			if strings.EqualFold(column.Name, reference.name) {
				return i, nil
			}
		}

		return 0, informationColumnError()
	}

	if statement.count {
		p.description.Columns = []schema.Column{{Name: statement.countAlias, Type: record.TypeInt64}}
	} else if statement.star {
		p.description.Columns = slices.Clone(p.columns)
		for i := range p.columns {
			p.projections = append(p.projections, i)
		}
	} else {
		for _, projection := range statement.projections {
			i, err := resolve(projection.reference)
			if err != nil {
				return nil, err
			}

			if projection.alias == "" {
				return nil, sessionSyntax()
			}

			column := p.columns[i]
			column.Name = projection.alias
			p.description.Columns = append(p.description.Columns, column)
			p.projections = append(p.projections, i)
		}
	}

	for _, order := range statement.orders {
		index := -1

		if order.reference.qualifier == "" {
			for i, column := range p.description.Columns {
				if strings.EqualFold(column.Name, order.reference.name) {
					if index != -1 {
						return nil, informationColumnError()
					}

					index = p.projections[i]
				}
			}
		}

		if index == -1 {
			var err error

			index, err = resolve(order.reference)
			if err != nil {
				return nil, err
			}
		}

		p.orders = append(p.orders, informationSort{column: index, descending: order.descending})
	}

	argument := 0

	for _, predicate := range statement.predicates {
		i, err := resolve(predicate.reference)
		if err != nil {
			return nil, err
		}

		column := p.columns[i]
		if predicate.operation == "like" && column.Type != record.TypeText {
			return nil, informationBindingError()
		}

		value := record.NewNull()

		switch {
		case predicate.parameter:
			parameter := column
			parameter.Nullable = true
			p.description.Parameters = append(p.description.Parameters, parameter)

			if describe {
				continue
			}

			if argument >= len(args) {
				return nil, &Error{1210, "HY000", "Incorrect INFORMATION_SCHEMA parameter count"}
			}

			value = args[argument]
			argument++
		case predicate.database:
			value, err = record.NewText(database)
		case predicate.number:
			var number int64

			number, err = strconv.ParseInt(predicate.value, 10, 64)
			value = record.NewInt64(number)
		case !predicate.null && (predicate.operation == "equal" || predicate.operation == "like"):
			value, err = record.NewText(predicate.value)
		}

		if err != nil {
			return nil, informationBindingError()
		}

		if !value.IsNull() {
			if column.Type == record.TypeInt64 && value.Type() == record.TypeUint64 {
				number, err := value.Uint64()
				if err != nil || number > math.MaxInt64 {
					return nil, informationBindingError()
				}

				value = record.NewInt64(int64(number))
			}

			if value.Type() != column.Type {
				return nil, informationBindingError()
			}

			if value.Type() == record.TypeText {
				text, err := value.Text()
				if err != nil || len(text) > maxInformationFilterBytes {
					return nil, informationBindingError()
				}

				if predicate.operation == "like" && (len(text) > 64 || strings.ContainsRune(text, '\\')) {
					return nil, informationBindingError()
				}
			}
		}

		p.filters = append(p.filters, informationFilter{column: i, operation: predicate.operation, value: value})
	}

	if argument != len(args) {
		return nil, &Error{1210, "HY000", "Incorrect INFORMATION_SCHEMA parameter count"}
	}

	return p, nil
}

func (s *statementSession) information(ctx context.Context, statement *informationStatement, describe bool, args []record.Value) (result sql.Result, description sql.Description, err error) {
	defer func() {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = &Error{3024, "HY000", "INFORMATION_SCHEMA query interrupted"}
		}
	}()

	if !describe {
		s.endpoint.informationCommands.Add(1)
		defer func() {
			if err != nil {
				s.endpoint.informationErrors.Add(1)
			}
		}()
	}

	plan, err := compileInformation(statement, s.endpoint.config.Database, describe, args)
	if err != nil {
		return result, description, err
	}

	if s.endpoint.query.Catalog == nil {
		return result, description, sessionUnsupported()
	}

	tables, err := s.endpoint.query.Catalog(ctx, "")
	if err != nil {
		return result, description, err
	}

	if len(tables) > MaxCatalogTables {
		return result, description, resultLimit()
	}

	if describe {
		return result, plan.description, nil
	}

	var rows [][]record.Value

	scanned, matched, size := 0, 0, 0

	err = informationRows(statement.view, s.endpoint.config.Database, tables, func(row []record.Value) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		scanned++
		if scanned > maxInformationScanRows {
			return resultLimit()
		}

		if !plan.matches(row) {
			return nil
		}

		matched++

		if statement.count {
			return nil
		}

		if len(rows) >= maxResultRows {
			return resultLimit()
		}

		for _, value := range row {
			size += 8

			if value.Type() == record.TypeText {
				text, _ := value.Text()
				size += len(text)
			}
		}

		if size > maxResultBytes {
			return resultLimit()
		}

		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return result, description, err
	}

	result.Columns = plan.description.Columns

	if statement.count {
		if statement.limit > 0 && statement.offset == 0 {
			result.Rows = [][]record.Value{{record.NewInt64(int64(matched))}}
		}

		return result, plan.description, nil
	}

	if len(plan.orders) > 0 {
		slices.SortStableFunc(rows, func(a, b []record.Value) int {
			for _, order := range plan.orders {
				compared := informationCompare(a[order.column], b[order.column])
				if compared != 0 {
					if order.descending {
						return -compared
					}

					return compared
				}
			}

			return 0
		})
	}

	if err := ctx.Err(); err != nil {
		return sql.Result{}, description, err
	}

	start := min(statement.offset, len(rows))

	end := start + min(statement.limit, len(rows)-start)
	for _, row := range rows[start:end] {
		projected := make([]record.Value, len(plan.projections))
		for i, column := range plan.projections {
			projected[i] = row[column]
		}

		result.Rows = append(result.Rows, projected)
	}

	return result, plan.description, nil
}

func (p *informationPlan) matches(row []record.Value) bool {
	for _, filter := range p.filters {
		value := row[filter.column]
		switch filter.operation {
		case "null":
			if !value.IsNull() {
				return false
			}
		case "not_null":
			if value.IsNull() {
				return false
			}
		case "equal":
			if value.IsNull() || filter.value.IsNull() || informationCompare(value, filter.value) != 0 {
				return false
			}
		case "like":
			if value.IsNull() || filter.value.IsNull() {
				return false
			}

			text, _ := value.Text()

			pattern, _ := filter.value.Text()
			if !discoveryLike(text, pattern) {
				return false
			}
		}
	}

	return true
}

func informationCompare(a, b record.Value) int {
	if a.IsNull() {
		if b.IsNull() {
			return 0
		}

		return -1
	}

	if b.IsNull() {
		return 1
	}

	if a.Type() == record.TypeInt64 {
		x, _ := a.Int64()

		y, _ := b.Int64()
		if x < y {
			return -1
		}

		if x > y {
			return 1
		}

		return 0
	}

	x, _ := a.Text()
	y, _ := b.Text()
	return strings.Compare(x, y)
}
