package database

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/Noah-Wilderom/godb/internal/observe"
	"github.com/Noah-Wilderom/godb/pkg/pager"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

const (
	MaxBatchEntries = 64
	writeQueueSize  = 256
)

type Entry struct {
	Key   []byte
	Value []byte
}

type writeRequest struct {
	enqueued                  time.Time
	log                       *logRequest
	transaction               []*writeRequest
	readRevision              *uint64
	entries                   []Entry
	insertChecks              []int
	absentChecks              []absentCheck
	table                     *schema.Table
	delete                    bool
	countDeletedRows          bool
	deleted                   int
	update                    *RowInsertPlan
	patch                     *rowPatch
	affectedRows, matchedRows uint64
	deletePrefix              int
	matched                   bool
	rejection                 error
	result                    chan error
}

type absentCheck struct {
	entry int
	err   error
}

func (request *writeRequest) batchSize() int {
	if request.transaction != nil {
		return MaxBatchEntries
	}

	return len(request.entries)
}

func newDatabase(p pageStore, header *pager.DatabaseHeader) *Database {
	db := &Database{
		pager:  p,
		header: header,
		writes: make(chan *writeRequest, writeQueueSize),
		done:   make(chan struct{}),
	}
	db.metrics = newEngineMetrics(db)
	go db.runWriter()
	return db
}

func (db *Database) PutBatch(entries []Entry) error {
	return db.submitWrite(func() (*writeRequest, error) { return makeWriteRequest(entries) })
}

func (db *Database) submitWrite(build func() (*writeRequest, error)) (err error) {
	if db.metrics != nil {
		db.metrics.activeWrites.Inc()
		defer db.metrics.activeWrites.Dec()
		defer observe.Since(db.metrics.write, time.Now())
	}

	db.counters.writeRequests.Add(1)

	defer func() {
		if err != nil {
			db.counters.writeErrors.Add(1)
		}
	}()

	db.submitMu.RLock()

	if db.closing {
		db.submitMu.RUnlock()
		return ErrClosed
	}

	request, err := build()
	if err != nil || request == nil {
		if err == nil {
			db.mu.RLock()
			err = db.failed
			db.mu.RUnlock()
		}

		db.submitMu.RUnlock()
		return err
	}

	if db.metrics != nil {
		request.enqueued = time.Now()
	}

	db.writes <- request
	db.submitMu.RUnlock()
	return <-request.result
}

func makeWriteRequest(entries []Entry) (*writeRequest, error) {
	if len(entries) > MaxBatchEntries {
		return nil, fmt.Errorf("got %d entries, maximum is %d: %w", len(entries), MaxBatchEntries, ErrBatchTooLarge)
	}

	for i, entry := range entries {
		if isReplicationKey(entry.Key) {
			return nil, ErrReservedKey
		}

		if err := pager.ValidateEntry(entry.Key, entry.Value); err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
	}

	if len(entries) == 0 {
		return nil, nil
	}

	owned := make([]Entry, len(entries))
	for i, entry := range entries {
		owned[i] = Entry{Key: bytes.Clone(entry.Key), Value: bytes.Clone(entry.Value)}
	}

	return &writeRequest{entries: owned, result: make(chan error, 1)}, nil
}

func (db *Database) runWriter() {
	defer close(db.done)

	var pending *writeRequest

	for {
		first := pending
		pending = nil

		if first == nil {
			first = <-db.writes
			if first == nil {
				return
			}
		}

		var requests [MaxBatchEntries]*writeRequest

		requests[0] = first
		count := 1
		entries := first.batchSize()

	drain:
		for entries < MaxBatchEntries {
			select {
			case next := <-db.writes:
				if next == nil {
					break drain
				}

				if entries+next.batchSize() > MaxBatchEntries {
					pending = next
					break drain
				}

				requests[count] = next
				count++
				entries += next.batchSize()
			default:
				break drain
			}
		}

		if db.metrics != nil {
			for _, request := range requests[:count] {
				if !request.enqueued.IsZero() {
					observe.Since(db.metrics.queueWait, request.enqueued)
				}
			}
		}

		err := db.applyBatch(requests[:count])
		for _, request := range requests[:count] {
			if request.rejection != nil {
				request.result <- request.rejection
			} else {
				request.result <- err
			}
		}
	}
}

func (db *Database) applyBatch(requests []*writeRequest) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.failed != nil {
		return db.failed
	}

	if len(requests) == 1 && requests[0].log != nil {
		return db.applyLogRequest(requests[0])
	}

	if db.applied.Index != 0 && (len(requests) != 1 || requests[0].transaction == nil || len(requests[0].transaction) != 0) {
		return ErrReplicatedWriteRequired
	}

	base := db.pager
	previousHeader := *db.header
	atomic := len(requests) == 1 && requests[0].transaction != nil
	committed := false

	if atomic {
		if revision := requests[0].readRevision; revision != nil && *revision != db.revision {
			return ErrTransactionConflict
		}

		requests = requests[0].transaction
		if len(requests) == 0 {
			return nil
		}
	}

	var staged *stagedPages

	if db.wal != nil || atomic {
		var err error

		staged, err = newStagedPages(base)
		if err == nil && staged.size != db.header.PageCount*uint64(db.header.PageSize) {
			err = fmt.Errorf("database size changed outside writer: %w", pager.ErrPagerCorruptPage)
		}

		if err != nil {
			db.failed = fmt.Errorf("%w: %w", ErrWriteFailed, err)
			return db.failed
		}

		db.pager = staged
		defer func() {
			db.pager = base
			if !committed {
				db.header = &previousHeader
			}
		}()
	}

	db.counters.writeBatches.Add(1)

	var leaves map[uint64]*pager.BPlusLeaf

	if len(requests) > 1 || len(requests[0].entries) > 1 || len(requests[0].insertChecks) > 0 {
		leaves = make(map[uint64]*pager.BPlusLeaf)
	}

	var written, deleted uint64

	for _, request := range requests {
		changed, removed, rejection, err := db.applyWriteRequest(request, leaves)
		request.rejection = rejection

		if err != nil {
			db.failed = fmt.Errorf("%w: %w", ErrWriteFailed, err)
			return db.failed
		}

		if request.rejection != nil {
			if atomic {
				return request.rejection
			}

			continue
		}

		written += changed
		deleted += removed
	}

	if written == 0 {
		return nil
	}

	db.pager = base

	var commitErr error

	if staged != nil {
		commitErr = db.commitStaged(staged)
	} else {
		commitErr = db.flush()
	}

	if commitErr != nil {
		db.failed = fmt.Errorf("%w: %w", ErrWriteFailed, commitErr)
		return db.failed
	}

	db.counters.writes.Add(written)
	db.counters.deletes.Add(deleted)
	db.revision++
	committed = true
	return nil
}

func (db *Database) applyWriteRequest(request *writeRequest, leaves map[uint64]*pager.BPlusLeaf) (written, deleted uint64, rejection, err error) {
	rejection, err = db.checkWriteRequest(request, leaves)
	if err != nil || rejection != nil {
		return 0, 0, rejection, err
	}

	for i, entry := range request.entries {
		remove := request.delete || i < request.deletePrefix

		changed := true
		if remove {
			changed, err = db.applyDelete(entry.Key, leaves)
		} else {
			err = db.applyPut(entry.Key, entry.Value, leaves)
		}

		if err != nil {
			return 0, 0, nil, err
		}

		if changed {
			written++

			if remove {
				deleted++
				request.deleted++
			}
		}
	}

	return written, deleted, nil, nil
}

func (db *Database) checkWriteRequest(request *writeRequest, leaves map[uint64]*pager.BPlusLeaf) (rejection, err error) {
	if request.table != nil {
		if _, err := db.requireTableLocked(*request.table); err != nil {
			if errors.Is(err, ErrTableNotFound) || errors.Is(err, ErrSchemaMismatch) {
				return err, nil
			}

			return nil, err
		}
	}

	if request.delete {
		return nil, db.prepareDelete(request, leaves)
	}

	if request.patch != nil {
		return db.preparePatch(request, leaves)
	}

	if request.update != nil {
		return db.prepareUpdate(request, leaves)
	}

	rejection, err = db.checkAbsent(request, leaves)
	if err != nil || rejection != nil {
		return rejection, err
	}

	return db.checkInsert(request, leaves)
}

func (db *Database) checkAbsent(request *writeRequest, leaves map[uint64]*pager.BPlusLeaf) (rejection, err error) {
	for _, check := range request.absentChecks {
		location, err := db.findLeafWithCache(request.entries[check.entry].Key, leaves)
		if err != nil {
			return nil, fmt.Errorf("check absent entry %d: %w", check.entry, err)
		}

		if _, _, found := location.leaf.Get(request.entries[check.entry].Key); found {
			return check.err, nil
		}
	}

	return nil, nil
}
