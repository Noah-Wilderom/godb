package server

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/replication"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func TestSQLExplainLeaderChangeAndQuorum(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		c.start(t, id)
	}

	leader := c.leader(t)
	s := c.members[leader]
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "CREATE TABLE t(id BIGINT PRIMARY KEY,score BIGINT,INDEX by_score(score))"))
	plan := prepareSQL(t, s, "EXPLAIN SELECT id FROM t WHERE score>=10 ORDER BY score LIMIT 0")
	noMatch := prepareSQL(t, s, "EXPLAIN SELECT COUNT(*) FROM t WHERE score=NULL")
	require.Equal(t, sql.Explain, plan.Kind())
	_, err := plan.Command()
	require.ErrorIs(t, err, sql.ErrUnsupported)
	before, err := s.db.AppliedPosition()
	require.NoError(t, err)
	result := executeSQL(t, s, uuid.Nil(), plan)
	require.Len(t, result.Rows, 1)
	after, err := s.db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = c.members[leader%3+1].ExecuteSQL(context.Background(), uuid.Nil(), plan)
	require.ErrorIs(t, err, replication.ErrNotLeader)
	require.NoError(t, s.Close())
	next := c.members[c.leader(t)]
	executeSQL(t, next, uuid.NewV4(), prepareSQL(t, next, "INSERT INTO t(id,score) VALUES(1,20)"))
	require.Equal(t, result, executeSQL(t, next, uuid.Nil(), plan))
	require.Len(t, executeSQL(t, next, uuid.Nil(), noMatch).Rows, 1)

	for _, member := range c.members {
		if member != next {
			require.NoError(t, member.Close())
		}
	}

	for _, p := range []*SQLPlan{plan, noMatch} {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		result, err := next.ExecuteSQL(ctx, uuid.Nil(), p)
		cancel()
		require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, replication.ErrNotLeader), "%v", err)
		require.Empty(t, result.Rows)
	}

	require.Zero(t, testutil.CollectAndCount(next.metrics.sqlSelects))
	require.Zero(t, testutil.CollectAndCount(next.metrics.sqlAggregates))
	metrics, err := next.metrics.registry.Gather()
	require.NoError(t, err)

	var success, failures uint64

	for _, family := range metrics {
		if family.GetName() != "godb_sql_statement_duration_seconds" {
			continue
		}

		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}

			if labels["statement"] != "explain" {
				continue
			}

			if labels["outcome"] == "ok" {
				success += metric.GetHistogram().GetSampleCount()
			} else {
				failures += metric.GetHistogram().GetSampleCount()
			}
		}
	}

	require.Equal(t, uint64(2), success)

	expectedFailures := uint64(2)
	if next == c.members[leader%3+1] {
		expectedFailures++
	}

	require.Equal(t, expectedFailures, failures)
}
