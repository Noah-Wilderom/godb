package database

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func rowInsertFixture() (schema.Table, schema.Row) {
	return schema.Table{
		ID: 1, Name: "users", PrimaryKeyColumnID: 1,
		Columns: []schema.Column{
			{ID: 1, Name: "id", Type: recordpkg.TypeUint64},
			{ID: 2, Name: "name", Type: recordpkg.TypeBytes, Nullable: true},
			{ID: 3, Name: "score", Type: recordpkg.TypeInt64},
		},
		Indexes: []schema.Index{
			{ID: 9, Name: "by_name", ColumnIDs: []uint32{2}},
			{ID: 3, Name: "by_score", ColumnIDs: []uint32{3}, Unique: true},
			{ID: 7, Name: "unique_name", ColumnIDs: []uint32{2}, Unique: true},
		},
	}, schema.Row{1: recordpkg.NewUint64(42), 2: recordpkg.NewBytes([]byte("Noah")), 3: recordpkg.NewInt64(7)}
}

func cloneRowPlanTable(table schema.Table) schema.Table {
	table.Columns = slices.Clone(table.Columns)
	table.Indexes = slices.Clone(table.Indexes)

	for i := range table.Indexes {
		table.Indexes[i].ColumnIDs = slices.Clone(table.Indexes[i].ColumnIDs)
	}

	return table
}

func TestPlanRowInsert_EntriesAndOrder(t *testing.T) {
	table, row := rowInsertFixture()
	beforeTable := cloneRowPlanTable(table)
	beforeRow := maps.Clone(row)
	plan, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	require.Equal(t, beforeTable, table)
	require.Equal(t, beforeRow, row)
	key, err := schema.EncodePrimaryKey(table, row[1])
	require.NoError(t, err)
	value, err := schema.EncodeRow(table, row)
	require.NoError(t, err)
	require.Equal(t, Entry{Key: key, Value: value}, plan.Primary)
	decoded, err := schema.DecodeRow(table, plan.Primary.Value)
	require.NoError(t, err)
	require.Equal(t, row, decoded)
	require.Len(t, plan.Indexes, 3)

	for i, id := range []uint32{3, 7, 9} {
		expected, err := schema.EncodeIndexEntry(table, id, row)
		require.NoError(t, err)
		require.Equal(t, expected, plan.Indexes[i])
		require.Equal(t, id, binary.BigEndian.Uint32(plan.Indexes[i].Key[5:9]))
		require.Equal(t, plan.Primary.Key, plan.Indexes[i].Value)
		require.NoError(t, pager.ValidateEntry(plan.Indexes[i].Key, plan.Indexes[i].Value))
	}

	require.True(t, plan.Indexes[0].RequiresUniqueCheck)
	require.True(t, plan.Indexes[1].RequiresUniqueCheck)
	require.False(t, plan.Indexes[2].RequiresUniqueCheck)
	slices.Reverse(table.Indexes)
	slices.Reverse(table.Columns)
	reordered, err := PlanRowInsert(table, maps.Clone(row))
	require.NoError(t, err)
	require.Equal(t, plan, reordered)
}

func TestPlanRowInsert_NullAndNoIndexes(t *testing.T) {
	table, row := rowInsertFixture()
	row[2] = recordpkg.NewNull()
	plan, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	require.True(t, plan.Indexes[0].RequiresUniqueCheck)
	require.False(t, plan.Indexes[1].RequiresUniqueCheck)
	require.False(t, plan.Indexes[2].RequiresUniqueCheck)
	table.Indexes = nil
	primaryOnly, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	require.Equal(t, plan.Primary, primaryOnly.Primary)
	require.Empty(t, primaryOnly.Indexes)
}

func TestPlanRowInsert_BatchLimit(t *testing.T) {
	table, row := rowInsertFixture()
	table.Indexes = nil

	for i := uint32(1); i <= MaxBatchEntries; i++ {
		table.Indexes = append(table.Indexes, schema.Index{ID: i, Name: fmt.Sprintf("index_%d", i), ColumnIDs: []uint32{2}})
	}

	tooMany := table
	table.Indexes = table.Indexes[:MaxBatchEntries-1]
	plan, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	require.Len(t, plan.Indexes, MaxBatchEntries-1)
	plan, err = PlanRowInsert(tooMany, row)
	require.ErrorIs(t, err, ErrBatchTooLarge)
	require.Equal(t, RowInsertPlan{}, plan)
}

func TestPlanRowInsert_PrimarySizeBoundary(t *testing.T) {
	const entryCapacity = 4096 - 3 - 4

	table, row := rowInsertFixture()
	table.Indexes = nil
	row[2] = recordpkg.NewBytes(nil)
	base, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	available := entryCapacity - len(base.Primary.Key) - len(base.Primary.Value)
	row[2] = recordpkg.NewBytes(bytes.Repeat([]byte{'x'}, available))
	plan, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	require.Equal(t, entryCapacity, len(plan.Primary.Key)+len(plan.Primary.Value))
	row[2] = recordpkg.NewBytes(bytes.Repeat([]byte{'x'}, available+1))
	plan, err = PlanRowInsert(table, row)
	require.ErrorIs(t, err, pager.ErrPagerEntryTooLarge)
	require.ErrorContains(t, err, "primary row entry")
	require.Equal(t, RowInsertPlan{}, plan)
}

func TestPlanRowInsert_SecondarySizeBoundary(t *testing.T) {
	const entryCapacity = 4096 - 3 - 4

	table, row := rowInsertFixture()
	table.Indexes = table.Indexes[:2]
	row[2] = recordpkg.NewBytes(nil)
	base, err := schema.EncodeIndexEntry(table, 9, row)
	require.NoError(t, err)
	zeros := (entryCapacity - len(base.Key) - len(base.Value)) / 2
	row[2] = recordpkg.NewBytes(make([]byte, zeros))
	plan, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	require.Equal(t, entryCapacity, len(plan.Indexes[1].Key)+len(plan.Indexes[1].Value))

	for _, size := range []int{zeros + 1, 3000} {
		row[2] = recordpkg.NewBytes(make([]byte, size))
		key, err := schema.EncodePrimaryKey(table, row[1])
		require.NoError(t, err)
		value, err := schema.EncodeRow(table, row)
		require.NoError(t, err)
		require.NoError(t, pager.ValidateEntry(key, value), "primary row still fits")
		plan, err := PlanRowInsert(table, row)
		require.ErrorIs(t, err, pager.ErrPagerEntryTooLarge)
		require.ErrorContains(t, err, "secondary index ID 9")
		require.Equal(t, RowInsertPlan{}, plan, "discard the primary and earlier valid secondary entry")
	}
}

func TestPlanRowInsert_InvalidInput(t *testing.T) {
	for _, change := range []func(*schema.Table, schema.Row){
		func(table *schema.Table, _ schema.Row) { table.ID = 0 },
		func(table *schema.Table, _ schema.Row) { table.Indexes[0].ColumnIDs = []uint32{99} },
		func(_ *schema.Table, row schema.Row) { delete(row, 2) },
		func(_ *schema.Table, row schema.Row) { row[1] = recordpkg.NewNull() },
		func(_ *schema.Table, row schema.Row) { row[2] = recordpkg.NewInt64(1) },
		func(_ *schema.Table, row schema.Row) { row[99] = recordpkg.NewNull() },
	} {
		table, row := rowInsertFixture()
		change(&table, row)
		plan, err := PlanRowInsert(table, row)
		require.Error(t, err)
		require.Equal(t, RowInsertPlan{}, plan)
	}

	table, _ := rowInsertFixture()
	plan, err := PlanRowInsert(table, nil)
	require.Error(t, err)
	require.Equal(t, RowInsertPlan{}, plan)
}

func TestPlanRowInsert_Ownership(t *testing.T) {
	table, row := rowInsertFixture()
	plan, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	want, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	row[2] = recordpkg.NewBytes([]byte("changed"))
	table.Indexes[0].ColumnIDs[0] = 1
	require.Equal(t, want, plan)
	clear(plan.Primary.Key)
	clear(plan.Primary.Value)

	for i := range plan.Indexes {
		require.Equal(t, want.Indexes[i], plan.Indexes[i])
		clear(plan.Indexes[i].Key)
		require.Equal(t, want.Indexes[i].Value, plan.Indexes[i].Value)
		clear(plan.Indexes[i].Value)
	}

	table, row = rowInsertFixture()
	again, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	require.Equal(t, want, again)
}
