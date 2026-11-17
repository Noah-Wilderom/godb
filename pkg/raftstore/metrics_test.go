package raftstore

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3"
)

func TestStoreMetricsTrackSavesAndRecovery(t *testing.T) {
	store, path := testStore(t)
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(store.Metrics()))
	require.NoError(t, store.Save(raft.Ready{}))
	require.Zero(t, testutil.ToFloat64(store.metrics.saves))
	seedStore(t, store)
	require.Equal(t, float64(1), testutil.ToFloat64(store.metrics.saves))
	require.Equal(t, float64(3), testutil.ToFloat64(store.metrics.last))
	require.Equal(t, float64(store.offset), testutil.ToFloat64(store.metrics.bytes))
	require.ErrorIs(t, store.Save(raft.Ready{HardState: testHard(1, 2, 1)}), ErrInvalidUpdate)
	require.Equal(t, float64(1), testutil.ToFloat64(store.metrics.errors))
	_, err := registry.Gather()
	require.NoError(t, err)
	require.NoError(t, store.Close())
	reopened, err := Open(path, testIdentity())
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	require.Equal(t, float64(3), testutil.ToFloat64(reopened.metrics.last))
	require.Equal(t, float64(reopened.offset), testutil.ToFloat64(reopened.metrics.bytes))
	require.Zero(t, testutil.ToFloat64(reopened.metrics.saves))
}

func TestMetricsScrapeWhileJournalIsLocked(t *testing.T) {
	store, _ := testStore(t)
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(store.Metrics()))
	store.mu.Lock()
	defer store.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := registry.Gather(); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("scrape waited for journal lock")
	}
}
