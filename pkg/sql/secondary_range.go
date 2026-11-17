package sql

import "github.com/Noah-Wilderom/godb/pkg/schema"

func (p *Plan) prepareSecondaryRange() error {
	if p.primaryRange != nil || (!p.tableScan && (p.indexID == 0 || p.unique)) {
		return nil
	}

	values := p.lookupValues()

	var (
		best   *schema.Index
		bounds *keyRange
	)

	bestPrefix := 0

	for i := range p.table.Indexes {
		candidate := &p.table.Indexes[i]
		prefix := 0

		for _, id := range candidate.ColumnIDs {
			if _, exists := values[id]; !exists {
				break
			}

			prefix++
		}

		if prefix == len(candidate.ColumnIDs) || (!p.tableScan && prefix+1 <= len(p.keys)) {
			continue
		}

		candidateBounds, err := p.rangeBounds(candidate.ColumnIDs[prefix])
		if err != nil {
			return err
		}

		if candidateBounds == nil {
			continue
		}

		if best != nil && !betterIndex(*candidate, prefix, *best, bestPrefix, values) {
			continue
		}

		best, bestPrefix, bounds = candidate, prefix, candidateBounds
	}

	if best == nil {
		return nil
	}

	p.secondaryRange = bounds
	p.indexID = best.ID
	p.tableScan, p.unique = false, false

	p.keys = nil
	for _, id := range best.ColumnIDs[:bestPrefix] {
		p.keys = append(p.keys, values[id])
	}

	return nil
}
