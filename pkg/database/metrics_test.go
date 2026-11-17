package database

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestEngineMetricsExposeDurableWork(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(db.Metrics()))
	require.NoError(t, db.Put([]byte("private-key"), []byte("private-value")))
	_, found, err := db.Get([]byte("private-key"))
	require.NoError(t, err)
	require.True(t, found)
	metrics, err := registry.Gather()
	require.NoError(t, err)

	names := make(map[string]bool)
	for _, metric := range metrics {
		names[metric.GetName()] = true
		require.NotContains(t, metric.String(), "private-key")
		require.NotContains(t, metric.String(), "private-value")

		if metric.GetName() == "godb_database_write_requests_total" {
			require.Equal(t, float64(db.Stats().WriteRequests), metric.Metric[0].Counter.GetValue())
		}

		if metric.GetName() == "godb_wal_sync_duration_seconds" {
			require.Equal(t, uint64(3), metric.Metric[0].Histogram.GetSampleCount())
		}
	}

	for _, name := range []string{"godb_database_get_duration_seconds", "godb_database_write_request_duration_seconds", "godb_database_sync_duration_seconds", "godb_wal_sync_duration_seconds", "godb_database_file_bytes"} {
		require.True(t, names[name], name)
	}

	require.Equal(t, float64(8192), testutil.ToFloat64(db.metrics.bytes))
	require.NoError(t, db.Close())
	_, _, err = db.Get([]byte("private-key"))
	require.ErrorIs(t, err, ErrClosed)
	_, err = registry.Gather()
	require.NoError(t, err)
}

func TestMetricsScrapeWhileDatabaseWriterIsLocked(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(db.Metrics()))
	db.mu.Lock()
	defer db.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := registry.Gather(); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("scrape waited for database lock")
	}
}
