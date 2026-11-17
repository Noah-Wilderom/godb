package database

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Noah-Wilderom/godb/internal/observe"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type WriteTx struct {
	started      time.Time
	mu           sync.Mutex
	db           *Database
	requests     []*writeRequest
	reserved     int
	err          error
	finished     bool
	readRevision *uint64
	view         *Database
	viewApplied  int
}

func (db *Database) BeginWrite() *WriteTx {
	tx := &WriteTx{db: db}
	if db.metrics != nil {
		tx.started = time.Now()
		db.metrics.activeTransactions.Inc()
	}

	return tx
}

func (tx *WriteTx) Put(key, value []byte) error {
	return tx.PutBatch([]Entry{{Key: key, Value: value}})
}

func (tx *WriteTx) PutBatch(entries []Entry) error {
	return tx.add(func() (*writeRequest, error) { return makeWriteRequest(entries) })
}

func (tx *WriteTx) Delete(key []byte) error {
	return tx.DeleteBatch([][]byte{key})
}

func (tx *WriteTx) DeleteBatch(keys [][]byte) error {
	return tx.add(func() (*writeRequest, error) { return makeDeleteRequest(keys) })
}

func (tx *WriteTx) CreateTable(table schema.Table) error {
	return tx.add(func() (*writeRequest, error) { return makeCreateTableRequest(table) })
}

func (tx *WriteTx) InsertRow(table schema.Table, row schema.Row) error {
	return tx.add(func() (*writeRequest, error) { return makeRowInsertRequest(table, row) })
}

func (tx *WriteTx) UpdateRow(table schema.Table, row schema.Row) error {
	return tx.add(func() (*writeRequest, error) { return makeRowUpdateRequest(table, row) })
}

func (tx *WriteTx) DeleteRow(table schema.Table, primaryKey recordpkg.Value) error {
	return tx.add(func() (*writeRequest, error) { return makeRowDeleteRequest(table, primaryKey) })
}

func (tx *WriteTx) add(build func() (*writeRequest, error)) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.finished || tx.db == nil {
		return ErrTransactionClosed
	}

	if tx.err != nil {
		return tx.err
	}

	request, err := build()
	if err != nil {
		tx.err = err
		return err
	}

	if request == nil {
		return nil
	}

	if tx.reserved+len(request.entries) > MaxBatchEntries {
		tx.err = fmt.Errorf("transaction reserves more than %d key mutations: %w", MaxBatchEntries, ErrBatchTooLarge)
		return tx.err
	}

	tx.reserved += len(request.entries)
	tx.requests = append(tx.requests, request)
	return nil
}

func (tx *WriteTx) Commit() (err error) {
	tx.mu.Lock()

	if tx.finished || tx.db == nil {
		tx.mu.Unlock()
		return ErrTransactionClosed
	}

	tx.finished = true
	requests, err := tx.requests, tx.err
	readRevision := tx.readRevision
	tx.requests = nil
	tx.view = nil
	tx.mu.Unlock()
	defer func() {
		outcome := "committed"
		if errors.Is(err, ErrTransactionConflict) {
			outcome = "conflict"
		} else if err != nil {
			outcome = "error"
		}

		tx.finishMetrics(outcome)
	}()

	if err != nil {
		return err
	}

	return tx.db.submitWrite(func() (*writeRequest, error) {
		if len(requests) == 0 && readRevision == nil {
			return nil, nil
		}

		if requests == nil {
			requests = make([]*writeRequest, 0)
		}

		return &writeRequest{
			transaction:  requests,
			readRevision: readRevision,
			result:       make(chan error, 1),
		}, nil
	})
}

func (tx *WriteTx) Rollback() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.finished || tx.db == nil {
		return ErrTransactionClosed
	}

	tx.finished = true
	tx.requests = nil
	tx.view = nil
	tx.finishMetrics("rolled_back")
	return nil
}

func (tx *WriteTx) finishMetrics(outcome string) {
	if tx.db.metrics == nil || tx.started.IsZero() {
		return
	}

	m := tx.db.metrics
	m.activeTransactions.Dec()
	m.transactions.WithLabelValues(outcome).Inc()
	observe.Since(m.transaction, tx.started)
}
