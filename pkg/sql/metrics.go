package sql

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Noah-Wilderom/godb/internal/observe"
)

type metrics struct {
	group                            observe.Group
	selects, aggregates              *prometheus.CounterVec
	active                           prometheus.Gauge
	duration                         *prometheus.HistogramVec
	rows, affected, matched, deleted prometheus.Counter
}

func newMetrics() *metrics {
	m := &metrics{
		aggregates: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "godb_sql_aggregate_total", Help: "Prepared local aggregate SELECT executions by function and outcome."}, []string{"function", "outcome"}),
		selects:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "godb_sql_select_total", Help: "Prepared local SELECT executions by access path and outcome; includes NULL short circuits."}, []string{"access", "outcome"}),
		active:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "godb_sql_statements_in_flight", Help: "Local SQL executions not yet returned, including planning and storage wait."}),
		duration:   prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "godb_sql_statement_duration_seconds", Help: "Local SQL execution latency by bounded statement kind and outcome.", Buckets: prometheus.DefBuckets}, []string{"statement", "outcome"}),
		rows:       prometheus.NewCounter(prometheus.CounterOpts{Name: "godb_sql_rows_returned_total", Help: "Rows returned by successful local SQL reads."}),
		deleted:    prometheus.NewCounter(prometheus.CounterOpts{Name: "godb_sql_rows_deleted_total", Help: "Rows removed by successful local SQL DELETE executions; excludes missing rows."}),
		matched:    prometheus.NewCounter(prometheus.CounterOpts{Name: "godb_sql_rows_matched_total", Help: "Rows matched by successful local SQL UPDATE executions, including unchanged rows."}),
		affected:   prometheus.NewCounter(prometheus.CounterOpts{Name: "godb_sql_rows_affected_total", Help: "Rows inserted, changed, or deleted by successful local SQL commands; excludes unchanged or missing rows."}),
	}
	m.group = observe.Group{m.aggregates, m.selects, m.active, m.duration, m.rows, m.affected, m.matched, m.deleted}
	return m
}
