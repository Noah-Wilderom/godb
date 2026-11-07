package database

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func TestDatabase_PutPersistsTreeSplits(t *testing.T) {
	for _, name := range treeSplitCases() {
		t.Run(name, func(t *testing.T) {
			fixture := newTreeSplitFixture(t, name)
			expected, err := os.ReadFile(fixture.path)
			require.NoError(t, err)

			for _, update := range fixture.want.updates {
				copy(expected[update.pageID*4096:], update.page)
			}

			for _, page := range fixture.want.appendedPages {
				expected = append(expected, page...)
			}

			headerPage, err := pager.EncodeHeader(&fixture.want.header)
			require.NoError(t, err)
			copy(expected, headerPage)

			require.NoError(t, fixture.db.Put(fixture.insert.key, fixture.insert.value))
			assert.Equal(t, fixture.want.header, *fixture.db.header)
			assertFileContents(t, fixture.path, expected)
			require.NoError(t, fixture.db.Close())
			require.NoError(t, Validate(fixture.path))
			reopened, err := Open(fixture.path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = reopened.Close() })

			for i, entry := range fixture.entries {
				value, found, err := reopened.Get(entry.key)
				require.NoError(t, err)
				require.True(t, found, "entry %d", i)
				assert.True(t, bytes.Equal(entry.value, value), "entry %d", i)
			}

			require.NoError(t, reopened.Put(fixture.insert.key, []byte("updated")))
			assert.Equal(t, fixture.want.header, *reopened.header)
			require.NoError(t, reopened.Close())
			again, err := Open(fixture.path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = again.Close() })
			value, found, err := again.Get(fixture.insert.key)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, []byte("updated"), value)
		})
	}
}

func TestDatabase_PutRepeatedInternalSplits(t *testing.T) {
	for _, order := range []string{"Ascending", "Descending", "Mixed"} {
		t.Run(order, func(t *testing.T) {
			path := writeDatabaseFixture(t, validDatabaseBytes())
			db, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			entries := make([]testEntry, 80)
			for i := range entries {
				key := append([]byte(fmt.Sprintf("%04d", i)), bytes.Repeat([]byte{'k'}, 996)...)
				entries[i] = testEntry{key: key, value: bytes.Repeat([]byte{byte(i)}, 1000)}
			}

			inserted := make([]bool, len(entries))
			for i := range entries {
				index := i

				switch order {
				case "Descending":
					index = len(entries) - 1 - i
				case "Mixed":
					index = i * 37 % len(entries)
				}

				entry := entries[index]
				require.NoError(t, db.Put(entry.key, entry.value))
				inserted[index] = true

				if (i+1)%10 == 0 {
					require.NoError(t, db.Close())
					db, err = Open(path)
					require.NoError(t, err)

					for j, present := range inserted {
						if !present {
							continue
						}

						value, found, err := db.Get(entries[j].key)
						require.NoError(t, err)
						require.True(t, found, "entry %d after insert %d", j, i)
						require.Equal(t, entries[j].value, value)
					}
				}
			}

			location, err := db.findLeaf(entries[0].key)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, len(location.parents), 2, "must exercise internal splits")
			require.NoError(t, Validate(path))
		})
	}
}

func TestWriteTreeSplit_RejectsPrewriteFailures(t *testing.T) {
	for _, name := range []string{"InvalidRoot", "InvalidPageSize", "UnsupportedFormat", "PageCountTooLarge", "PartialExtraPage", "ExtraPage", "ClosedPager"} {
		t.Run(name, func(t *testing.T) {
			fixture := newTreeSplitFixture(t, "CascadeToNewRoot")
			plan, err := fixture.db.prepareTreeSplit(fixture.location)
			require.NoError(t, err)
			headerBefore := *fixture.db.header

			var want error

			switch name {
			case "InvalidRoot":
				plan.header.RootPageID = plan.header.PageCount
			case "InvalidPageSize":
				plan.header.PageSize = 1
			case "UnsupportedFormat":
				plan.header.FormatVersion = 2
			case "PageCountTooLarge":
				plan.header.PageCount = uint64(math.MaxInt64/4096) + 1
			case "PartialExtraPage":
				require.NoError(t, os.Truncate(fixture.path, int64(headerBefore.PageCount*4096)+1))
				want = pager.ErrPagerCorruptPage
			case "ExtraPage":
				require.NoError(t, os.Truncate(fixture.path, int64(headerBefore.PageCount*4096)+4096))
				want = pager.ErrPagerCorruptPage
			case "ClosedPager":
				require.NoError(t, fixture.db.Close())
				want = os.ErrClosed
			}

			before, err := os.ReadFile(fixture.path)
			require.NoError(t, err)
			err = fixture.db.writeTreeSplit(plan)
			require.Error(t, err)

			if want != nil {
				require.ErrorIs(t, err, want)
			}

			assert.Equal(t, headerBefore, *fixture.db.header)
			assertFileContents(t, fixture.path, before)
		})
	}
}
