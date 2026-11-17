package raftstore

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Noah-Wilderom/godb/internal/observe"
)

type storeMetrics struct {
	observe.Group

	save, sync    prometheus.Histogram
	saves, errors prometheus.Counter
	bytes, last   prometheus.Gauge
}

func newMetrics() *storeMetrics {
	m := &storeMetrics{
		save:   observe.Duration("raft_store_save", "Raft Save call latency including empty updates and validation."),
		sync:   observe.Duration("raft_store_sync", "Raft journal sync latency during nonempty Save attempts."),
		saves:  prometheus.NewCounter(prometheus.CounterOpts{Name: "godb_raft_store_saves_total", Help: "Successful nonempty Raft journal saves."}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{Name: "godb_raft_store_errors_total", Help: "Raft Save calls returning an error."}),
		bytes:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "godb_raft_store_bytes", Help: "Last recovered or successfully synced journal size."}),
		last:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "godb_raft_store_last_index", Help: "Last recovered or successfully saved log index."}),
	}
	m.Group = observe.Group{m.save, m.sync, m.saves, m.errors, m.bytes, m.last}
	return m
}

func (s *Store) Metrics() prometheus.Collector { return s.metrics }
