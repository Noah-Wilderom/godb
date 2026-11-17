package sql

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func explainColumns() []schema.Column {
	return []schema.Column{
		{Name: "table", Type: record.TypeText},
		{Name: "access", Type: record.TypeText},
		{Name: "index", Type: record.TypeText, Nullable: true},
		{Name: "equality_prefix_columns", Type: record.TypeInt64},
		{Name: "range_column", Type: record.TypeText, Nullable: true},
		{Name: "lower_bound", Type: record.TypeText, Nullable: true},
		{Name: "upper_bound", Type: record.TypeText, Nullable: true},
		{Name: "candidate_limit", Type: record.TypeInt64},
		{Name: "result_limit", Type: record.TypeInt64},
		{Name: "result_offset", Type: record.TypeInt64},
		{Name: "extra", Type: record.TypeText},
	}
}

func explainText(text string) record.Value {
	value, _ := record.NewText(text)
	return value
}

func (p *Plan) explainResult() Result {
	selected := p.explained
	indexName, rangeName := record.NewNull(), record.NewNull()
	bounds := selected.primaryRange

	var rangeID uint32

	if !selected.tableScan {
		if selected.indexID == 0 {
			indexName = explainText("PRIMARY")

			if bounds != nil {
				rangeID = selected.table.PrimaryKeyColumnID
			}
		} else {
			for _, index := range selected.table.Indexes {
				if index.ID != selected.indexID {
					continue
				}

				indexName = explainText(index.Name)

				if selected.secondaryRange != nil {
					bounds = selected.secondaryRange
					rangeID = index.ColumnIDs[len(selected.keys)]
				}

				break
			}
		}
	}

	for _, column := range selected.table.Columns {
		if column.ID == rangeID {
			rangeName = explainText(column.Name)
			break
		}
	}

	lower, upper := record.NewNull(), record.NewNull()
	if bounds != nil {
		lower, upper = explainText("unbounded"), explainText("unbounded")
		if bounds.lower != nil {
			lower = explainText("exclusive")
			if bounds.lower.Inclusive {
				lower = explainText("inclusive")
			}
		}

		if bounds.upper != nil {
			upper = explainText("exclusive")
			if bounds.upper.Inclusive {
				upper = explainText("inclusive")
			}
		}
	}

	candidateLimit := int64(MaxSelectRows)
	if selected.noMatch {
		candidateLimit = 0
	} else if selected.AccessPath() == "primary" || selected.unique {
		candidateLimit = 1
	}

	return Result{Columns: slices.Clone(p.columns), Rows: [][]record.Value{{
		explainText(selected.table.Name), explainText(selected.AccessPath()), indexName, record.NewInt64(int64(len(selected.keys))),
		rangeName, lower, upper, record.NewInt64(candidateLimit), record.NewInt64(int64(selected.limit)), record.NewInt64(int64(selected.offset)),
		explainText(selected.explainExtra()),
	}}}
}

func (p *Plan) explainExtra() string {
	var extra []string

	if p.noMatch {
		extra = append(extra, "No matching rows (NULL predicate)")
	}

	if len(p.filters) > 0 {
		extra = append(extra, fmt.Sprintf("Recheck %d predicates", len(p.filters)))
	}

	if len(p.orders) > 0 {
		extra = append(extra, "Sort captured rows")
	}

	if p.count != nil {
		name := "*"

		for _, column := range p.table.Columns {
			if column.ID == p.count.columnID {
				name = "`" + strings.ReplaceAll(column.Name, "`", "``") + "`"
				break
			}
		}

		extra = append(extra, "COUNT("+name+")")
	}

	return strings.Join(extra, "; ")
}
