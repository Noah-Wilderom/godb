package database

import (
	"encoding/binary"
	"uuid"
)

func (db *Database) validateReplicationMetadata() error {
	position, err := db.readLogPosition()
	if err != nil {
		return err
	}

	var lastIndex, lastTerm uint64

	err = walkValidationTree(db.pager, db.header, func(entry Entry) error {
		if !isReplicationKey(entry.Key) {
			return nil
		}

		if position.Index == 0 || len(entry.Key) < 3 {
			return ErrInvalidReplicationState
		}

		switch entry.Key[2] {
		case 0:
			if len(entry.Key) != 3 {
				return ErrInvalidReplicationState
			}
		case 1:
			if len(entry.Key) != 19 {
				return ErrInvalidReplicationState
			}

			var id uuid.UUID

			copy(id[:], entry.Key[3:])

			if id == uuid.Nil() {
				return ErrInvalidReplicationState
			}

			record, _, err := db.readRequestRecord(id)
			if err != nil {
				return err
			}

			if record.index > position.Index {
				return ErrInvalidReplicationState
			}
		case 2:
			if len(entry.Key) != 11 {
				return ErrInvalidReplicationState
			}

			index := binary.BigEndian.Uint64(entry.Key[3:])
			if index == 0 || index <= lastIndex || index-lastIndex != 1 || index > position.Index {
				return ErrInvalidReplicationState
			}

			record, err := decodeLogRecord(entry.Value)
			if err != nil {
				return err
			}

			if record.term < lastTerm {
				return ErrInvalidReplicationState
			}

			if err := db.validateLogRequest(index, record); err != nil {
				return err
			}

			lastIndex, lastTerm = index, record.term
		default:
			return ErrInvalidReplicationState
		}

		return nil
	})
	if err != nil {
		return err
	}

	if lastIndex != position.Index || lastTerm != position.Term {
		return ErrInvalidReplicationState
	}

	return nil
}
