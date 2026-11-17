package replication

import (
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type cluster struct {
	members  map[uint64]*fixture
	isolated uint64
	results  map[uint64][]Result
	reads    map[uint64][]ReadResult
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	c := &cluster{members: make(map[uint64]*fixture), results: make(map[uint64][]Result)}
	c.reads = make(map[uint64][]ReadResult)

	for id := uint64(1); id <= 3; id++ {
		f := newFixture(t, id)
		f.start(t, 3, 1, 2)
		c.members[id] = f
	}

	output, err := c.members[1].node.Campaign()
	require.NoError(t, err)
	c.deliver(t, 1, output)
	state, err := c.members[1].node.Status()
	require.NoError(t, err)
	require.True(t, state.IsLeader)
	return c
}

func (c *cluster) deliver(t *testing.T, id uint64, output Output) {
	t.Helper()
	c.results[id] = append(c.results[id], output.Results...)
	c.reads[id] = append(c.reads[id], output.Reads...)

	queue := output.Messages
	for steps := 0; len(queue) != 0; steps++ {
		require.Less(t, steps, 10000)
		message := queue[0]
		queue = queue[1:]

		if message.Raft.GetFrom() == c.isolated || message.Raft.GetTo() == c.isolated {
			continue
		}

		destination := message.Raft.GetTo()
		next, err := c.members[destination].node.Step(message)
		require.NoError(t, err)
		c.results[destination] = append(c.results[destination], next.Results...)
		c.reads[destination] = append(c.reads[destination], next.Reads...)
		queue = append(queue, next.Messages...)
	}
}

func (c *cluster) propose(t *testing.T, leader uint64, id uuid.UUID, command []byte) {
	t.Helper()
	output, err := c.members[leader].node.Propose(id, command)
	require.NoError(t, err)
	c.deliver(t, leader, output)
}

func (c *cluster) tick(t *testing.T, id uint64) {
	t.Helper()
	output, err := c.members[id].node.Tick()
	require.NoError(t, err)
	c.deliver(t, id, output)
}

func TestClusterReplicatesRowsIndexesAndRejections(t *testing.T) {
	c := newCluster(t)
	table := schema.Table{
		ID: 1, Name: "users", PrimaryKeyColumnID: 1,
		Columns: []schema.Column{{ID: 1, Name: "id", Type: record.TypeUint64}, {ID: 2, Name: "email", Type: record.TypeBytes}},
		Indexes: []schema.Index{{ID: 1, Name: "email", ColumnIDs: []uint32{2}, Unique: true}},
	}
	row := schema.Row{1: record.NewUint64(1), 2: record.NewBytes([]byte("a@example.com"))}
	encoded, err := database.EncodeCommand(database.Command{Operations: []database.CommandOperation{
		{Kind: database.CommandCreateTable, Table: &table},
		{Kind: database.CommandInsertRow, Table: &table, Row: row},
	}})
	require.NoError(t, err)
	c.propose(t, 1, uuid.UUID{2}, encoded)
	conflict, err := database.EncodeCommand(database.Command{Operations: []database.CommandOperation{
		{Kind: database.CommandPut, Key: []byte("rollback"), Value: []byte("value")},
		{Kind: database.CommandInsertRow, Table: &table, Row: schema.Row{1: record.NewUint64(2), 2: row[2]}},
	}})
	require.NoError(t, err)
	c.propose(t, 1, uuid.UUID{3}, conflict)

	var position database.LogPosition

	for id := uint64(1); id <= 3; id++ {
		f := c.members[id]
		results := c.results[id]
		require.Len(t, results, 2)
		require.Equal(t, database.CommandOK, results[0].Command.Code)
		require.Equal(t, database.CommandUniqueConflict, results[1].Command.Code)
		stored, found, err := f.db.GetRowByUniqueIndex(table, 1, row[2])
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, row, stored)
		_, found, err = f.db.Get([]byte("rollback"))
		require.NoError(t, err)
		require.False(t, found)
		current, err := f.db.AppliedPosition()
		require.NoError(t, err)

		if id == 1 {
			position = current
		}

		require.Equal(t, position, current)
		f.reopen(t)
		require.Zero(t, f.db.Stats().Writes)
	}
}

func TestClusterRequiresQuorumAndRecoversPartition(t *testing.T) {
	c := newCluster(t)
	c.propose(t, 1, uuid.UUID{2}, commandPut(t, "key", "initial"))
	c.isolated = 1
	orphanID := uuid.UUID{3}
	output, err := c.members[1].node.Propose(orphanID, commandPut(t, "key", "orphan"))
	require.NoError(t, err)
	require.Empty(t, output.Results)
	c.deliver(t, 1, output)
	assertValue(t, c.members[1].db, "key", "initial")

	var leader uint64

	for tick := 0; tick < 100 && leader == 0; tick++ {
		for id := uint64(2); id <= 3; id++ {
			c.tick(t, id)
			status, err := c.members[id].node.Status()
			require.NoError(t, err)

			if status.IsLeader {
				leader = id
			}
		}
	}

	require.NotZero(t, leader)
	c.propose(t, leader, uuid.UUID{4}, commandPut(t, "key", "majority"))

	c.isolated = 0
	for range 12 {
		c.tick(t, leader)
	}

	c.propose(t, leader, uuid.UUID{5}, commandPut(t, "key", "healed"))

	for id := uint64(1); id <= 3; id++ {
		f := c.members[id]
		assertValue(t, f.db, "key", "healed")
		_, found, err := f.db.AppliedRequest(orphanID)
		require.NoError(t, err)
		require.False(t, found)
		position, err := f.db.AppliedPosition()
		require.NoError(t, err)
		hard, _, err := f.store.InitialState()
		require.NoError(t, err)
		require.Equal(t, hard.GetCommit(), position.Index)
	}

	c.propose(t, leader, orphanID, commandPut(t, "key", "retried"))

	for id := uint64(1); id <= 3; id++ {
		assertValue(t, c.members[id].db, "key", "retried")
	}
}
