package database

import (
	"crypto/sha256"
	"fmt"
	"uuid"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

type LogPosition struct {
	GroupID uuid.UUID
	Index   uint64
	Term    uint64
}

type LogEntry struct {
	GroupID   uuid.UUID
	Index     uint64
	Term      uint64
	RequestID uuid.UUID
	Command   []byte
}

type logRequest struct {
	position  LogPosition
	requestID uuid.UUID
	digest    [sha256.Size]byte
	outcome   CommandResult
}

func (db *Database) AppliedPosition() (LogPosition, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if db.closed {
		return LogPosition{}, ErrClosed
	}

	if db.failed != nil {
		return LogPosition{}, db.failed
	}

	return db.applied, nil
}

func (db *Database) AppliedRequest(id uuid.UUID) (CommandResult, bool, error) {
	if id == uuid.Nil() {
		return CommandResult{}, false, ErrInvalidLogEntry
	}

	db.mu.RLock()
	defer db.mu.RUnlock()

	stored, found, err := db.readRequestRecord(id)
	if err != nil || !found {
		return CommandResult{}, false, err
	}

	if stored.index > db.applied.Index {
		return CommandResult{}, false, ErrInvalidReplicationState
	}

	return CommandResult{Code: stored.code, AffectedRows: stored.affectedRows, MatchedRows: stored.matchedRows}, true, nil
}

func (db *Database) ApplyLogEntry(entry LogEntry) (CommandResult, error) {
	if entry.GroupID == uuid.Nil() || entry.Index == 0 || entry.Term == 0 ||
		(len(entry.Command) == 0) != (entry.RequestID == uuid.Nil()) {
		return CommandResult{}, ErrInvalidLogEntry
	}

	requests := make([]*writeRequest, 0)

	if len(entry.Command) != 0 {
		_, prepared, err := decodeCommand(entry.Command)
		if err != nil {
			return CommandResult{}, err
		}

		requests = prepared
	}

	logged := &logRequest{
		position:  LogPosition{GroupID: entry.GroupID, Index: entry.Index, Term: entry.Term},
		requestID: entry.RequestID,
		digest:    sha256.Sum256(entry.Command),
	}

	err := db.submitWrite(func() (*writeRequest, error) {
		return &writeRequest{log: logged, transaction: requests, result: make(chan error, 1)}, nil
	})
	if err != nil {
		return CommandResult{}, err
	}

	return logged.outcome, nil
}

func (db *Database) applyLogRequest(request *writeRequest) error {
	logged := request.log
	position := logged.position

	previous := db.applied
	if previous.Index != 0 && position.GroupID != previous.GroupID {
		return ErrLogGroupMismatch
	}

	if position.Index <= previous.Index {
		stored, err := db.readLogRecord(position.Index)
		if err != nil {
			return db.failLogWrite(err)
		}

		if err := db.validateLogRequest(position.Index, stored); err != nil {
			return db.failLogWrite(err)
		}

		if stored.term != position.Term || stored.requestID != logged.requestID || stored.digest != logged.digest {
			return ErrLogConflict
		}

		logged.outcome = CommandResult{Code: stored.code, AffectedRows: stored.affectedRows, MatchedRows: stored.matchedRows}
		return nil
	}

	if position.Index-previous.Index != 1 || position.Term < previous.Term {
		return ErrLogOrder
	}

	outcome := CommandResult{Code: CommandOK}
	newRequest := logged.requestID != uuid.Nil()

	apply := newRequest
	if newRequest {
		stored, found, err := db.readRequestRecord(logged.requestID)
		if err != nil {
			return db.failLogWrite(err)
		}

		if found {
			newRequest, apply = false, false

			if stored.index > previous.Index {
				return db.failLogWrite(ErrInvalidReplicationState)
			}

			outcome = CommandResult{Code: stored.code, AffectedRows: stored.affectedRows, MatchedRows: stored.matchedRows}
			if stored.digest != logged.digest {
				outcome = CommandResult{Code: CommandRequestIDConflict}
			}
		}
	}

	base := db.pager
	previousHeader := *db.header

	staged, err := newStagedPages(base)
	if err != nil {
		return db.failLogWrite(err)
	}

	if staged.size != previousHeader.PageCount*uint64(previousHeader.PageSize) {
		return db.failLogWrite(pager.ErrPagerCorruptPage)
	}

	committed := false
	db.pager = staged
	defer func() {
		db.pager = base
		if !committed {
			db.header = &previousHeader
		}
	}()
	db.counters.writeBatches.Add(1)
	leaves := make(map[uint64]*pager.BPlusLeaf)

	var written, deleted uint64

	if apply {
		for _, child := range request.transaction {
			changed, removed, rejection, err := db.applyWriteRequest(child, leaves)
			if err != nil {
				return db.failLogWrite(err)
			}

			if rejection != nil {
				outcome, err = commandResultForError(rejection)
				if err != nil {
					return db.failLogWrite(err)
				}

				staged, err = newStagedPages(base)
				if err != nil {
					return db.failLogWrite(err)
				}

				db.pager, db.header = staged, &previousHeader
				clear(leaves)
				written, deleted = 0, 0
				break
			}

			outcome.AffectedRows += child.affectedRows
			outcome.MatchedRows += child.matchedRows
			written += changed
			deleted += removed
		}
	}

	if newRequest {
		value := encodeRequestRecord(requestRecord{index: position.Index, digest: logged.digest, code: outcome.Code, affectedRows: outcome.AffectedRows, matchedRows: outcome.MatchedRows})
		if err := db.applyPut(requestRecordKey(logged.requestID), value, leaves); err != nil {
			return db.failLogWrite(err)
		}

		written++
	}

	value := encodeLogRecord(logRecord{term: position.Term, requestID: logged.requestID, digest: logged.digest, code: outcome.Code, affectedRows: outcome.AffectedRows, matchedRows: outcome.MatchedRows})
	if err := db.applyPut(logRecordKey(position.Index), value, leaves); err != nil {
		return db.failLogWrite(err)
	}

	if err := db.applyPut(logPositionKey(), encodeLogPosition(position), leaves); err != nil {
		return db.failLogWrite(err)
	}

	db.pager = base
	if err := db.commitStaged(staged); err != nil {
		return db.failLogWrite(err)
	}

	db.applied = position
	db.revision++
	db.counters.writes.Add(written + 2)
	db.counters.deletes.Add(deleted)
	logged.outcome = outcome
	committed = true
	return nil
}

func (db *Database) failLogWrite(err error) error {
	db.failed = fmt.Errorf("%w: %w", ErrWriteFailed, err)
	return db.failed
}
