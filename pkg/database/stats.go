package database

import "sync/atomic"

type Stats struct {
	Reads           uint64
	ReadErrors      uint64
	WriteRequests   uint64
	WriteErrors     uint64
	Writes          uint64
	WriteBatches    uint64
	SplitOperations uint64
	PageReads       uint64
	PageWrites      uint64
	PageAppends     uint64
	Syncs           uint64
	SyncErrors      uint64
}

type counters struct {
	reads           atomic.Uint64
	readErrors      atomic.Uint64
	writeRequests   atomic.Uint64
	writeErrors     atomic.Uint64
	writes          atomic.Uint64
	writeBatches    atomic.Uint64
	splitOperations atomic.Uint64
	pageReads       atomic.Uint64
	pageWrites      atomic.Uint64
	pageAppends     atomic.Uint64
	syncs           atomic.Uint64
	syncErrors      atomic.Uint64
}

func (db *Database) Stats() Stats {
	return Stats{
		Reads:           db.counters.reads.Load(),
		ReadErrors:      db.counters.readErrors.Load(),
		WriteRequests:   db.counters.writeRequests.Load(),
		WriteErrors:     db.counters.writeErrors.Load(),
		Writes:          db.counters.writes.Load(),
		WriteBatches:    db.counters.writeBatches.Load(),
		SplitOperations: db.counters.splitOperations.Load(),
		PageReads:       db.counters.pageReads.Load(),
		PageWrites:      db.counters.pageWrites.Load(),
		PageAppends:     db.counters.pageAppends.Load(),
		Syncs:           db.counters.syncs.Load(),
		SyncErrors:      db.counters.syncErrors.Load(),
	}
}
