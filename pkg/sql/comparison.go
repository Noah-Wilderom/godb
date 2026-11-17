package sql

import (
	"bytes"
	"cmp"
	"errors"
	"strings"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
)

func (f rowFilter) comparisonMatches(value record.Value) (bool, error) {
	if value.IsNull() || f.value.IsNull() {
		return false, nil
	}

	if value.Type() != f.value.Type() {
		return false, database.ErrInvalidData
	}

	compared, err := compareValues(value, f.value)
	if err != nil {
		return false, err
	}

	switch f.operation {
	case predicateLess:
		return compared < 0, nil
	case predicateLessEqual:
		return compared <= 0, nil
	case predicateGreater:
		return compared > 0, nil
	case predicateGreaterEqual:
		return compared >= 0, nil
	default:
		return false, ErrUnsupported
	}
}

func compareValues(a, b record.Value) (int, error) {
	switch a.Type() {
	case record.TypeInt64:
		left, leftErr := a.Int64()
		right, rightErr := b.Int64()
		return cmp.Compare(left, right), errors.Join(leftErr, rightErr)
	case record.TypeUint64:
		left, leftErr := a.Uint64()
		right, rightErr := b.Uint64()
		return cmp.Compare(left, right), errors.Join(leftErr, rightErr)
	case record.TypeText:
		left, leftErr := a.Text()
		right, rightErr := b.Text()
		return strings.Compare(left, right), errors.Join(leftErr, rightErr)
	case record.TypeBytes:
		left, leftErr := a.Bytes()
		right, rightErr := b.Bytes()
		return bytes.Compare(left, right), errors.Join(leftErr, rightErr)
	case record.TypeUUID:
		left, leftErr := a.UUID()
		right, rightErr := b.UUID()
		return bytes.Compare(left[:], right[:]), errors.Join(leftErr, rightErr)
	case record.TypeBool:
		left, leftErr := a.Bool()
		right, rightErr := b.Bool()

		compared := 0
		if left != right {
			compared = -1
			if left {
				compared = 1
			}
		}

		return compared, errors.Join(leftErr, rightErr)
	default:
		return 0, ErrType
	}
}
