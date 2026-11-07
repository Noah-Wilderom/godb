package database

import (
	"bytes"
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/pager"
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
	entries []Entry
	result  chan error
}

func newDatabase(p pageStore, header *pager.DatabaseHeader) *Database {
	db := &Database{
		pager:  p,
		header: header,
		writes: make(chan *writeRequest, writeQueueSize),
		done:   make(chan struct{}),
	}
	go db.runWriter()
	return db
}

func (db *Database) PutBatch(entries []Entry) (err error) {
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

	request, err := makeWriteRequest(entries)
	if err != nil || request == nil {
		if err == nil {
			db.mu.RLock()
			err = db.failed
			db.mu.RUnlock()
		}

		db.submitMu.RUnlock()
		return err
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
		entries := len(first.entries)

	drain:
		for entries < MaxBatchEntries {
			select {
			case next := <-db.writes:
				if next == nil {
					break drain
				}

				if entries+len(next.entries) > MaxBatchEntries {
					pending = next
					break drain
				}

				requests[count] = next
				count++
				entries += len(next.entries)
			default:
				break drain
			}
		}

		err := db.applyBatch(requests[:count])
		for _, request := range requests[:count] {
			request.result <- err
		}
	}
}

func (db *Database) applyBatch(requests []*writeRequest) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.failed != nil {
		return db.failed
	}

	db.counters.writeBatches.Add(1)

	var leaves map[uint64]*pager.BPlusLeaf

	if len(requests) > 1 || len(requests[0].entries) > 1 {
		leaves = make(map[uint64]*pager.BPlusLeaf)
	}

	var written uint64

	for _, request := range requests {
		for _, entry := range request.entries {
			if err := db.applyPut(entry.Key, entry.Value, leaves); err != nil {
				db.failed = fmt.Errorf("%w: %w", ErrWriteFailed, err)
				return db.failed
			}

			written++
		}
	}

	if err := db.flush(); err != nil {
		db.failed = fmt.Errorf("%w: %w", ErrWriteFailed, err)
		return db.failed
	}

	db.counters.writes.Add(written)
	return nil
}
