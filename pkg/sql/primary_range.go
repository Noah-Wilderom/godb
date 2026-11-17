package sql

import "github.com/Noah-Wilderom/godb/pkg/database"

type keyRange struct {
	lower, upper *database.KeyBound
}

func (p *Plan) preparePrimaryRange() error {
	if !p.tableScan && (p.indexID == 0 || p.unique) {
		return nil
	}

	bounds, err := p.rangeBounds(p.table.PrimaryKeyColumnID)
	if err != nil {
		return err
	}

	if bounds == nil {
		return nil
	}

	p.primaryRange = bounds
	p.tableScan, p.unique = false, false
	p.indexID, p.keys = 0, nil
	return nil
}

func (p *Plan) rangeBounds(columnID uint32) (*keyRange, error) {
	bounds := &keyRange{}

	for _, filter := range p.filters {
		if filter.columnID != columnID || filter.value.IsNull() {
			continue
		}

		var target **database.KeyBound

		lower := false

		switch filter.operation {
		case predicateGreater, predicateGreaterEqual:
			target, lower = &bounds.lower, true
		case predicateLess, predicateLessEqual:
			target = &bounds.upper
		default:
			continue
		}

		candidate := &database.KeyBound{Value: filter.value, Inclusive: filter.operation == predicateGreaterEqual || filter.operation == predicateLessEqual}
		if *target == nil {
			*target = candidate
			continue
		}

		compared, err := compareValues(candidate.Value, (*target).Value)
		if err != nil {
			return nil, err
		}

		if (lower && compared > 0) || (!lower && compared < 0) || (compared == 0 && !candidate.Inclusive) {
			*target = candidate
		}
	}

	if bounds.lower == nil && bounds.upper == nil {
		return nil, nil
	}

	return bounds, nil
}
