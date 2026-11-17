package replication

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadIndexRequiresQuorumWithoutAppending(t *testing.T) {
	c := newCluster(t)
	leader := c.members[1]
	before, err := leader.store.LastIndex()
	require.NoError(t, err)
	output, err := leader.node.ReadIndex(1)
	require.NoError(t, err)
	require.Empty(t, output.Reads)
	c.deliver(t, 1, output)
	require.Len(t, c.reads[1], 1)
	require.Equal(t, uint64(1), c.reads[1][0].ID)
	position, err := leader.db.AppliedPosition()
	require.NoError(t, err)
	require.GreaterOrEqual(t, position.Index, c.reads[1][0].Index)
	after, err := leader.store.LastIndex()
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = c.members[2].node.ReadIndex(1)
	require.ErrorIs(t, err, ErrNotLeader)
	_, err = leader.node.ReadIndex(0)
	require.ErrorIs(t, err, ErrInvalidRead)
	c.isolated = 1
	output, err = leader.node.ReadIndex(2)
	require.NoError(t, err)
	c.deliver(t, 1, output)
	require.Len(t, c.reads[1], 1)

	for range 30 {
		c.tick(t, 2)
		c.tick(t, 3)
	}

	state, err := leader.node.Status()
	require.NoError(t, err)
	require.True(t, state.IsLeader)
	output, err = leader.node.ReadIndex(3)
	require.NoError(t, err)
	c.deliver(t, 1, output)
	require.Len(t, c.reads[1], 1)
	c.isolated = 0

	for range 20 {
		for id := uint64(1); id <= 3; id++ {
			c.tick(t, id)
		}
	}

	state, err = leader.node.Status()
	require.NoError(t, err)
	require.False(t, state.IsLeader)
	require.Len(t, c.reads[1], 1)
}

func TestReadIndexSingleMemberAndClosed(t *testing.T) {
	f := newFixture(t, 1)
	f.start(t, 1)
	_, err := f.node.Campaign()
	require.NoError(t, err)
	output, err := f.node.ReadIndex(9)
	require.NoError(t, err)
	require.Len(t, output.Reads, 1)
	require.Equal(t, uint64(9), output.Reads[0].ID)
	state, err := f.node.Status()
	require.NoError(t, err)
	require.Equal(t, state.Applied, output.Reads[0].Index)
	f.node.Close()
	_, err = f.node.ReadIndex(10)
	require.ErrorIs(t, err, ErrClosed)
}
