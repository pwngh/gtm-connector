package postgres_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// sentences is a prepared brief whose card holds its account's record and this many invented sentence units,
// perNote to each invented note, all resting on one read of the account and the notes.
func sentences(tb testing.TB, f *postgrestest.Fixture, brief string, units, perNote int) postgres.CardPublication {
	tb.Helper()
	said := make([]string, units)
	for i := range said {
		said[i] = fmt.Sprintf("Invented sentence %d says the renewal moved by a week.", i+1)
	}
	notes := make([]postgres.RecordVersion, (units+perNote-1)/perNote)
	for i := range notes {
		id := fmt.Sprintf("note-%06d", i+1)
		notes[i] = postgrestest.Record("notes", id, "id", id,
			"text", strings.Join(said[i*perNote:min(units, (i+1)*perNote)], " "))
	}
	// The account comes first: unit 1 is its record, which the card's one line cites.
	records := append([]postgres.RecordVersion{postgrestest.Record("accounts", postgrestest.Account, "id",
		postgrestest.Account, "v", brief)}, notes...)
	pub, call, read := postgrestest.Prepared(tb, f.Store, f, brief+"-call", brief, f.Reader.ID,
		postgres.TokenID("raw-token"), records...)
	bound := map[string]string{}
	for _, u := range pub.Units {
		bound[u.RecordRef] = u.SourceRecordID
	}
	pub.Units = append(pub.Units[:1], make([]postgres.Unit, units)...)
	for i := range units {
		ref := notes[i/perNote].RecordRef()
		pub.Units[i+1] = postgres.Unit{N: i + 2, Kind: "sentence", RecordRef: ref, Tool: "get_account", Shown: "yes",
			ReceiptID: read.ID, SourceRecordID: bound[ref],
			Body: postgrestest.UnitBody(i+2, "sentence", ref, "get_account", "yes", map[string]any{"text": said[i]})}
	}
	pub.Bytes = postgrestest.CardBytes(tb, call, pub.AccountRef, brief, []postgres.Receipt{read}, records, pub.Units,
		pub.Gaps)
	return pub
}

// Publishing ten times the units takes about ten times as long, not a hundred: the database counts a card's
// units once at commit, compares each with the bytes once and checks each record a unit cites once, and the
// store finds each unit's record version without walking every version its read bound. Each size is timed
// twice and the faster run kept.
func TestPublishingGrowsLinearlyWithUnits(t *testing.T) {
	f := postgrestest.New(t)
	took := func(units int) time.Duration {
		var best time.Duration
		for i := range 2 {
			pub := sentences(t, f, fmt.Sprintf("linear-%d-%d", units, i), units, 10)
			start := time.Now()
			_, err := f.Store.PublishCard(ctx, pub)
			ok(t, err)
			if took := time.Since(start); i == 0 || took < best {
				best = took
			}
		}
		return best
	}
	small, large := took(1000), took(10000)
	ratio := float64(large) / float64(small)
	t.Logf("1,000 units took %v and 10,000 took %v, %.0f times as long", small, large, ratio)
	if ratio > 20 {
		t.Fail()
	}
}

// BenchmarkPublishCard publishes cards of invented sentence units, 100 to a note, at 2,000 and 20,000 units,
// and at 100,000 with GTM_BENCH_LARGE=1. commit-s/op is the part spent in COMMIT, where the deferred checks
// run. Against the Compose database:
//
//	GTM_BENCH_LARGE=1 scripts/host.sh test -run '^$' -bench PublishCard
func BenchmarkPublishCard(b *testing.B) {
	sizes := []int{2000, 20000}
	if os.Getenv("GTM_BENCH_LARGE") == "1" {
		sizes = append(sizes, 100000)
	}
	for _, units := range sizes {
		b.Run(fmt.Sprintf("units=%d", units), func(b *testing.B) {
			f := postgrestest.New(b)
			var sealed time.Time
			postgres.SetCrash(f.Store, func(point string) error {
				if point == "publish:sealed" {
					sealed = time.Now()
				}
				return nil
			})
			var commit time.Duration
			for i := 0; b.Loop(); i++ {
				b.StopTimer()
				pub := sentences(b, f, fmt.Sprintf("scale-%d", i), units, 100)
				b.StartTimer()
				if _, err := f.Store.PublishCard(ctx, pub); err != nil {
					b.Fatal(err)
				}
				commit += time.Since(sealed)
			}
			b.ReportMetric(commit.Seconds()/float64(b.N), "commit-s/op")
		})
	}
}
