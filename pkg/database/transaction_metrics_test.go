package database

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestTransactionMetricsTerminalOutcomes(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	committed := db.BeginWrite()
	rolledBack := db.BeginWrite()
	conflicted := db.BeginWrite()
	invalid := db.BeginWrite()
	require.Equal(t, float64(4), testutil.ToFloat64(db.metrics.activeTransactions))
	_, _, err := conflicted.Get([]byte("key"))
	require.NoError(t, err)
	require.NoError(t, committed.Put([]byte("key"), []byte("value")))
	require.NoError(t, committed.Commit())
	require.ErrorIs(t, committed.Commit(), ErrTransactionClosed)
	require.NoError(t, rolledBack.Rollback())
	require.ErrorIs(t, rolledBack.Rollback(), ErrTransactionClosed)
	require.ErrorIs(t, conflicted.Commit(), ErrTransactionConflict)
	require.Error(t, invalid.Put(make([]byte, 65536), nil))
	require.Error(t, invalid.Commit())
	require.Zero(t, testutil.ToFloat64(db.metrics.activeTransactions))

	for _, outcome := range []string{"committed", "rolled_back", "conflict", "error"} {
		require.Equal(t, float64(1), testutil.ToFloat64(db.metrics.transactions.WithLabelValues(outcome)), outcome)
	}

	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(db.Metrics()))
	metrics, err := registry.Gather()
	require.NoError(t, err)

	for _, metric := range metrics {
		if metric.GetName() == "godb_database_transaction_duration_seconds" {
			require.Equal(t, uint64(4), metric.Metric[0].Histogram.GetSampleCount())
		}
	}
}

func TestWritePressureMetricsWhileWriterBlocked(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	db.mu.Lock()
	locked := true
	defer func() {
		if locked {
			db.mu.Unlock()
		}
	}()
	done := make(chan error, 2)
	go func() { done <- db.Put([]byte("a"), []byte("value")) }()
	require.Eventually(t, func() bool {
		var sample dto.Metric

		if err := db.metrics.queueWait.Write(&sample); err != nil {
			return false
		}

		return sample.GetHistogram().GetSampleCount() == 1
	}, time.Second, time.Millisecond)
	go func() { done <- db.Put([]byte("b"), []byte("value")) }()
	require.Eventually(t, func() bool { return testutil.ToFloat64(db.metrics.activeWrites) == 2 && len(db.writes) == 1 }, time.Second, time.Millisecond)
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(db.Metrics()))
	_, err := registry.Gather()
	require.NoError(t, err)
	db.mu.Unlock()
	locked = false

	for range 2 {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("write did not finish")
		}
	}

	require.Zero(t, testutil.ToFloat64(db.metrics.activeWrites))
	metrics, err := registry.Gather()
	require.NoError(t, err)

	for _, metric := range metrics {
		switch metric.GetName() {
		case "godb_database_write_queue_wait_duration_seconds":
			require.Equal(t, uint64(2), metric.Metric[0].Histogram.GetSampleCount())
		case "godb_database_page_write_bytes_total":
			require.Equal(t, float64(db.Stats().PageWrites)*float64(db.header.PageSize), metric.Metric[0].Counter.GetValue())
		}
	}
}
