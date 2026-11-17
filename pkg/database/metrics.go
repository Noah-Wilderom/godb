package database

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Noah-Wilderom/godb/internal/observe"
)

type engineMetrics struct {
	observe.Group

	get, write, sync, queueWait, transaction prometheus.Histogram
	bytes                                    prometheus.Gauge
	activeWrites, activeTransactions         prometheus.Gauge
	transactions                             *prometheus.CounterVec
}

func newEngineMetrics(db *Database) *engineMetrics {
	m := &engineMetrics{get: observe.Duration("database_get", "Raw Get latency including lock wait."), write: observe.Duration("database_write_request", "Write submission latency including queue wait, persistence and rejection."), sync: observe.Duration("database_sync", "Database file sync latency, including failed attempts; excludes WAL syncs.")}
	m.bytes = prometheus.NewGauge(prometheus.GaugeOpts{Name: "godb_database_file_bytes", Help: "Database bytes from recovered metadata or the last successful database sync."})
	m.bytes.Set(float64(db.header.PageCount * uint64(db.header.PageSize)))
	m.queueWait = observe.Duration("database_write_queue_wait", "Write scheduling delay from enqueue attempt until the batch is dispatched; excludes batch execution and database lock wait.")
	m.transaction = observe.Duration("database_transaction", "Explicit WriteTx lifetime through Commit completion or Rollback, including application think time.")
	m.activeWrites = prometheus.NewGauge(prometheus.GaugeOpts{Name: "godb_database_writes_in_flight", Help: "submitWrite calls not yet returned, including building, queue wait and execution."})
	m.activeTransactions = prometheus.NewGauge(prometheus.GaugeOpts{Name: "godb_database_transactions_active", Help: "Explicit WriteTx handles not yet finished by Commit or Rollback; not automatic write batches."})

	m.transactions = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "godb_database_transactions_total", Help: "Explicit WriteTx terminal outcomes; commits include successful no-op commits, errors do not assert that a disk failure wrote no bytes."}, []string{"outcome"})
	for _, outcome := range []string{"committed", "rolled_back", "conflict", "error"} {
		m.transactions.WithLabelValues(outcome)
	}

	m.Group = observe.Group{m.get, m.write, m.sync, m.bytes, m.queueWait, m.transaction, m.activeWrites, m.activeTransactions, m.transactions}
	for _, metric := range []struct {
		name, help string
		value      func() float64
	}{
		{"reads", "Existing engine read-operation counter.", func() float64 { return float64(db.counters.reads.Load()) }},
		{"read_errors", "Existing engine read-error counter.", func() float64 { return float64(db.counters.readErrors.Load()) }},
		{"write_requests", "Write submissions, including validation errors and retries.", func() float64 { return float64(db.counters.writeRequests.Load()) }},
		{"write_errors", "Write submissions returning a Go error.", func() float64 { return float64(db.counters.writeErrors.Load()) }},
		{"writes", "Key writes, including internal metadata.", func() float64 { return float64(db.counters.writes.Load()) }},
		{"deletes", "Keys deleted.", func() float64 { return float64(db.counters.deletes.Load()) }},
		{"write_batches", "Writer batches processed.", func() float64 { return float64(db.counters.writeBatches.Load()) }},
		{"splits", "B+ tree split operations.", func() float64 { return float64(db.counters.splitOperations.Load()) }},
		{"page_reads", "Successful page reads outside staged overlays.", func() float64 { return float64(db.counters.pageReads.Load()) }},
		{"page_writes", "Successful physical page writes.", func() float64 { return float64(db.counters.pageWrites.Load()) }},
		{"page_appends", "Successful physical page appends.", func() float64 { return float64(db.counters.pageAppends.Load()) }},
		{"syncs", "Successful database file syncs.", func() float64 { return float64(db.counters.syncs.Load()) }},
		{"sync_errors", "Failed database file syncs.", func() float64 { return float64(db.counters.syncErrors.Load()) }},
	} {
		m.Group = append(m.Group, observe.Counter("database_"+metric.name, metric.help, metric.value))
	}

	m.Group = append(m.Group, observe.Gauge("database_write_queue_entries", "Requests currently buffered in the database writer queue.", func() float64 { return float64(len(db.writes)) }))
	pageSize := float64(db.header.PageSize)
	m.Group = append(m.Group,
		observe.Gauge("database_write_queue_capacity", "Maximum buffered write requests; not a connection limit.", func() float64 { return float64(cap(db.writes)) }),
		observe.Counter("database_page_read_bytes", "Successful page reads times page size; may be served by OS cache, not device I/O bytes.", func() float64 { return float64(db.counters.pageReads.Load()) * pageSize }),
		observe.Counter("database_page_write_bytes", "Successful physical page writes times page size, including appends; excludes WAL and OS/device amplification.", func() float64 { return float64(db.counters.pageWrites.Load()) * pageSize }),
	)
	return m
}

func (db *Database) Metrics() prometheus.Collector {
	group := observe.Group{}
	if db.metrics != nil {
		group = append(group, db.metrics)
	}

	if db.wal != nil && db.wal.metrics != nil {
		group = append(group, db.wal.metrics)
	}

	return group
}

type walMetrics struct {
	observe.Group

	sync   prometheus.Histogram
	errors prometheus.Counter
}

func newWALMetrics() *walMetrics {
	m := &walMetrics{sync: observe.Duration("wal_sync", "WAL file sync latency including recovery, clearing and commit."), errors: prometheus.NewCounter(prometheus.CounterOpts{Name: "godb_wal_sync_errors_total", Help: "Failed WAL sync attempts."})}
	m.Group = observe.Group{m.sync, m.errors}
	return m
}
