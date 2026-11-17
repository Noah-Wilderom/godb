package database

import (
	"crypto/sha256"
	"encoding/binary"
	"uuid"
)

const (
	replicationNamespace byte = 0x03
)

type logRecord struct {
	term                      uint64
	requestID                 uuid.UUID
	digest                    [sha256.Size]byte
	code                      CommandResultCode
	affectedRows, matchedRows uint64
}

type requestRecord struct {
	index                     uint64
	digest                    [sha256.Size]byte
	code                      CommandResultCode
	affectedRows, matchedRows uint64
}

func isReplicationKey(key []byte) bool {
	return len(key) >= 2 && key[0] == 0 && key[1] == replicationNamespace
}

func logPositionKey() []byte { return []byte{0, replicationNamespace, 0} }

func requestRecordKey(id uuid.UUID) []byte {
	return append([]byte{0, replicationNamespace, 1}, id[:]...)
}

func logRecordKey(index uint64) []byte {
	return binary.BigEndian.AppendUint64([]byte{0, replicationNamespace, 2}, index)
}

func encodeLogPosition(position LogPosition) []byte {
	encoded := make([]byte, 33)
	encoded[0] = 1
	copy(encoded[1:17], position.GroupID[:])
	binary.LittleEndian.PutUint64(encoded[17:25], position.Index)
	binary.LittleEndian.PutUint64(encoded[25:33], position.Term)
	return encoded
}

func decodeLogPosition(encoded []byte) (LogPosition, error) {
	if len(encoded) != 33 || encoded[0] != 1 {
		return LogPosition{}, ErrInvalidReplicationState
	}

	position := LogPosition{Index: binary.LittleEndian.Uint64(encoded[17:25]), Term: binary.LittleEndian.Uint64(encoded[25:33])}
	copy(position.GroupID[:], encoded[1:17])

	if position.GroupID == uuid.Nil() || position.Index == 0 || position.Term == 0 {
		return LogPosition{}, ErrInvalidReplicationState
	}

	return position, nil
}

func encodeLogRecord(record logRecord) []byte {
	encoded := make([]byte, 58)
	encoded[0] = 1
	binary.LittleEndian.PutUint64(encoded[1:9], record.term)
	copy(encoded[9:25], record.requestID[:])
	copy(encoded[25:57], record.digest[:])
	encoded[57] = encodeResultCode(record.code)
	return appendResultCounts(encoded, record.affectedRows, record.matchedRows)
}

func decodeLogRecord(encoded []byte) (logRecord, error) {
	if len(encoded) < 58 {
		return logRecord{}, ErrInvalidReplicationState
	}

	record := logRecord{term: binary.LittleEndian.Uint64(encoded[1:9]), code: decodeResultCode(encoded[57])}
	copy(record.requestID[:], encoded[9:25])
	copy(record.digest[:], encoded[25:57])

	counts, err := decodeResultCounts(encoded, 58, record.code)
	if err != nil {
		return logRecord{}, err
	}

	record.affectedRows, record.matchedRows = counts.AffectedRows, counts.MatchedRows

	if record.term == 0 || record.code == "" ||
		(record.requestID == uuid.Nil() && (record.code != CommandOK || record.matchedRows != 0 || record.digest != sha256.Sum256(nil))) {
		return logRecord{}, ErrInvalidReplicationState
	}

	return record, nil
}

func encodeRequestRecord(record requestRecord) []byte {
	encoded := make([]byte, 42)
	encoded[0] = 1
	binary.LittleEndian.PutUint64(encoded[1:9], record.index)
	copy(encoded[9:41], record.digest[:])
	encoded[41] = encodeResultCode(record.code)
	return appendResultCounts(encoded, record.affectedRows, record.matchedRows)
}

func decodeRequestRecord(encoded []byte) (requestRecord, error) {
	if len(encoded) < 42 {
		return requestRecord{}, ErrInvalidReplicationState
	}

	record := requestRecord{index: binary.LittleEndian.Uint64(encoded[1:9]), code: decodeResultCode(encoded[41])}
	copy(record.digest[:], encoded[9:41])

	counts, err := decodeResultCounts(encoded, 42, record.code)
	if err != nil {
		return requestRecord{}, err
	}

	record.affectedRows, record.matchedRows = counts.AffectedRows, counts.MatchedRows

	if record.index == 0 || record.code == "" || record.code == CommandRequestIDConflict {
		return requestRecord{}, ErrInvalidReplicationState
	}

	return record, nil
}

func encodeResultCode(code CommandResultCode) byte {
	switch code {
	case CommandOK:
		return 1
	case CommandDuplicatePrimaryKey:
		return 2
	case CommandUniqueConflict:
		return 3
	case CommandTableIDExists:
		return 4
	case CommandTableNameExists:
		return 5
	case CommandTableNotFound:
		return 6
	case CommandSchemaMismatch:
		return 7
	case CommandMutationLimit:
		return 8
	case CommandRequestIDConflict:
		return 9
	case CommandInvalidUpdate:
		return 10
	default:
		return 0
	}
}

func decodeResultCode(code byte) CommandResultCode {
	codes := [...]CommandResultCode{
		"", CommandOK, CommandDuplicatePrimaryKey, CommandUniqueConflict,
		CommandTableIDExists, CommandTableNameExists, CommandTableNotFound, CommandSchemaMismatch,
		CommandMutationLimit, CommandRequestIDConflict, CommandInvalidUpdate,
	}
	if int(code) >= len(codes) {
		return ""
	}

	return codes[code]
}

func (db *Database) readLogRecord(index uint64) (logRecord, error) {
	encoded, found, err := db.getLocked(logRecordKey(index))
	if err != nil {
		return logRecord{}, err
	}

	if !found {
		return logRecord{}, ErrInvalidReplicationState
	}

	return decodeLogRecord(encoded)
}

func (db *Database) readRequestRecord(id uuid.UUID) (requestRecord, bool, error) {
	encoded, found, err := db.getLocked(requestRecordKey(id))
	if err != nil || !found {
		return requestRecord{}, false, err
	}

	record, err := decodeRequestRecord(encoded)
	if err != nil {
		return requestRecord{}, false, err
	}

	first, err := db.readLogRecord(record.index)
	if err != nil {
		return requestRecord{}, false, err
	}

	if first.requestID != id || first.digest != record.digest || first.code != record.code || first.affectedRows != record.affectedRows || first.matchedRows != record.matchedRows {
		return requestRecord{}, false, ErrInvalidReplicationState
	}

	return record, true, nil
}

func (db *Database) readLogPosition() (LogPosition, error) {
	encoded, found, err := db.getLocked(logPositionKey())
	if err != nil {
		return LogPosition{}, err
	}

	if !found {
		entries, err := db.scanLocked([]byte{0, replicationNamespace}, []byte{0, replicationNamespace + 1}, 1)
		if err != nil {
			return LogPosition{}, err
		}

		if len(entries) != 0 {
			return LogPosition{}, ErrInvalidReplicationState
		}

		return LogPosition{}, nil
	}

	position, err := decodeLogPosition(encoded)
	if err != nil {
		return LogPosition{}, err
	}

	last, err := db.readLogRecord(position.Index)
	if err != nil {
		return LogPosition{}, err
	}

	if last.term != position.Term {
		return LogPosition{}, ErrInvalidReplicationState
	}

	if err := db.validateLogRequest(position.Index, last); err != nil {
		return LogPosition{}, err
	}

	return position, nil
}

func (db *Database) validateLogRequest(index uint64, record logRecord) error {
	if record.requestID == uuid.Nil() {
		return nil
	}

	request, found, err := db.readRequestRecord(record.requestID)
	if err != nil {
		return err
	}

	if !found || request.index > index {
		return ErrInvalidReplicationState
	}

	if request.digest == record.digest {
		if request.code != record.code || request.affectedRows != record.affectedRows || request.matchedRows != record.matchedRows {
			return ErrInvalidReplicationState
		}
	} else if record.code != CommandRequestIDConflict || request.index == index {
		return ErrInvalidReplicationState
	}

	return nil
}

func appendResultCounts(encoded []byte, affected, matched uint64) []byte {
	if matched == 0 {
		return encoded
	}

	encoded[0] = 2
	encoded = binary.LittleEndian.AppendUint64(encoded, affected)
	return binary.LittleEndian.AppendUint64(encoded, matched)
}

func decodeResultCounts(encoded []byte, base int, code CommandResultCode) (CommandResult, error) {
	result := CommandResult{Code: code}
	if encoded[0] == 1 && len(encoded) == base {
		return result, nil
	}

	if encoded[0] != 2 || len(encoded) != base+16 || code != CommandOK {
		return CommandResult{}, ErrInvalidReplicationState
	}

	result.AffectedRows = binary.LittleEndian.Uint64(encoded[base : base+8])

	result.MatchedRows = binary.LittleEndian.Uint64(encoded[base+8:])
	if result.MatchedRows == 0 || result.MatchedRows > MaxBatchEntries || result.AffectedRows > result.MatchedRows {
		return CommandResult{}, ErrInvalidReplicationState
	}

	return result, nil
}
