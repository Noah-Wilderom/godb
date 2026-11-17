package sql

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

const (
	MaxIndexes = database.MaxBatchEntries - 1
)

func (p *parser) indexDefinition(unique bool, name string) {
	if unique {
		if !p.word("KEY") {
			p.word("INDEX")
		}
	}

	if name == "" && p.peek().kind != '(' {
		name = p.name()
	}

	p.expect('(')
	columns := p.names()
	p.expect(')')
	p.addIndex(indexDefinition{name: name, columns: columns, unique: unique})
}

func (p *parser) addIndex(index indexDefinition) {
	if p.err != nil {
		return
	}

	if len(p.statement.indexes) >= MaxIndexes {
		p.err = ErrLimit
		return
	}

	p.statement.indexes = append(p.statement.indexes, index)
}

func prepareIndexes(definitions []indexDefinition, columns []schema.Column) ([]schema.Index, error) {
	if len(definitions) == 0 {
		return nil, nil
	}

	if len(definitions) > MaxIndexes {
		return nil, ErrLimit
	}

	byName := make(map[string]uint32, len(columns))
	for _, column := range columns {
		byName[column.Name] = column.ID
	}

	used := []string{"PRIMARY"}

	contains := func(name string) bool {
		for _, existing := range used {
			if strings.EqualFold(name, existing) {
				return true
			}
		}

		return false
	}
	for _, definition := range definitions {
		if definition.name == "" {
			continue
		}

		if contains(definition.name) {
			return nil, fmt.Errorf("index %q: %w", definition.name, ErrIndexName)
		}

		used = append(used, definition.name)
	}

	var indexes []schema.Index

	indexID := uint32(1)
	for _, definition := range definitions {
		index := schema.Index{ID: indexID, Name: definition.name, Unique: definition.unique}
		if len(definition.columns) == 0 {
			return nil, ErrColumn
		}

		if index.Name == "" {
			base := definition.columns[0]

			index.Name = base
			for suffix := 2; contains(index.Name); suffix++ {
				ending := "_" + strconv.Itoa(suffix)

				prefix := base[:min(len(base), MaxIdentifierBytes-len(ending))]
				for !utf8.ValidString(prefix) {
					prefix = prefix[:len(prefix)-1]
				}

				index.Name = prefix + ending
			}

			used = append(used, index.Name)
		}

		seen := make(map[uint32]bool, len(definition.columns))
		for _, name := range definition.columns {
			id, ok := byName[name]
			if !ok || seen[id] {
				return nil, fmt.Errorf("index %q column %q: %w", index.Name, name, ErrColumn)
			}

			seen[id] = true
			index.ColumnIDs = append(index.ColumnIDs, id)
		}

		indexes = append(indexes, index)
		indexID++
	}

	return indexes, nil
}
