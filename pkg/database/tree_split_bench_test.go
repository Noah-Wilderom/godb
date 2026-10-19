package database

import (
	"os"
	"testing"
)

func BenchmarkDatabase_PutTreeSplitSynced(b *testing.B) {
	for _, name := range []string{"InternalRoot", "GrandparentAbsorbs", "CascadeToNewRoot", "OversizedNewRoot"} {
		b.Run(name, func(b *testing.B) {
			fixture := newTreeSplitFixture(b, name)
			initial, err := os.ReadFile(fixture.path)
			if err != nil {
				b.Fatal(err)
			}
			header := *fixture.db.header
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				if err := os.WriteFile(fixture.path, initial, 0600); err != nil {
					b.Fatal(err)
				}
				*fixture.db.header = header
				b.StartTimer()
				if err := fixture.db.Put(fixture.insert.key, fixture.insert.value); err != nil {
					b.Fatal(err)
				}
				if *fixture.db.header != fixture.want.header {
					b.Fatal("unexpected tree split metadata")
				}
			}
		})
	}
}

func BenchmarkPrepareTreeSplitCached(b *testing.B) {
	for _, name := range treeSplitCases() {
		b.Run(name, func(b *testing.B) {
			fixture := newTreeSplitFixture(b, name)
			if _, err := fixture.db.prepareTreeSplit(fixture.location); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				plan, err := fixture.db.prepareTreeSplit(fixture.location)
				if err != nil {
					b.Fatal(err)
				}
				if plan.header != fixture.want.header || len(plan.updates) != len(fixture.want.updates) || len(plan.appendedPages) != len(fixture.want.appendedPages) {
					b.Fatal("unexpected tree split plan")
				}
			}
		})
	}
}
