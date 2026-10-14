package organizer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/state"
)

// The device number of a filesystem can change across reboots (btrfs,
// mergerfs, ZFS). The ledger must still find its entries then, so a file
// deleted from the library is not linked again and a full scan does not
// prune the ledger.
func TestLedgerSurvivesDeviceChange(t *testing.T) {
	e := newEnv(t, "jellyfin")
	write(t, filepath.Join(e.downloads, "Heat.1995.1080p.mkv"))
	if res, _ := e.org.Scan(context.Background()); len(res) != 1 || res[0].Status != jobs.StatusLinked {
		t.Fatalf("first scan = %+v", res)
	}
	lib := filepath.Join(e.movies, "Heat (1995)", "Heat (1995).mkv")
	os.Remove(lib)

	// Reload the ledger as if every entry had been recorded under another
	// device number.
	data, _ := e.org.Ledger().Export()
	var m map[string]LedgerEntry
	json.Unmarshal(data, &m)
	moved := map[string]LedgerEntry{}
	for k, v := range m {
		_, ino, _ := strings.Cut(k, ":")
		moved["4242:"+ino] = v
	}
	data, _ = json.Marshal(moved)
	if err := e.org.Ledger().Import(data); err != nil {
		t.Fatal(err)
	}

	res, _ := e.org.Scan(context.Background())
	if len(res) != 1 || !leftAlone(res[0]) {
		t.Fatalf("scan after device change = %+v", res)
	}
	if _, err := os.Stat(lib); err == nil {
		t.Error("a file deleted from the library was linked again")
	}
	data, _ = e.org.Ledger().Export()
	if strings.Contains(string(data), `"4242:`) || !strings.Contains(string(data), "Heat (1995)") {
		t.Errorf("entry not moved to the new device number: %s", data)
	}
}

// Entries recorded while the downloads folder was being listed survive the
// prune: they belong to files the listing missed.
func TestPruneKeepsEntriesRecordedSinceListing(t *testing.T) {
	l := NewLedger()
	p := filepath.Join(t.TempDir(), "a.mkv")
	os.WriteFile(p, []byte("x"), 0o644)
	fi, _ := os.Stat(p)
	before := time.Now()
	l.Put(fi, LedgerEntry{Lib: "movies", Dest: "A/A.mkv", Status: jobs.StatusLinked})
	l.Prune(nil, before)
	if _, ok := l.Lookup(fi); !ok {
		t.Error("an entry recorded after the listing started was pruned")
	}
	l.Prune(nil, time.Now())
	if _, ok := l.Lookup(fi); ok {
		t.Error("an entry for a file gone before the listing was kept")
	}

	pend := NewPending()
	pend.Update(Result{Source: "b.mkv", Status: jobs.StatusError, Error: "x"})
	pend.Prune(nil, before)
	if len(pend.List()) != 1 {
		t.Error("a pending item recorded after the listing started was pruned")
	}
}

// A full ledger fits in a state document, or loading it would fail and the
// ledger would restart empty (and deleted files would be linked again).
func TestFullLedgerFitsInState(t *testing.T) {
	e := LedgerEntry{
		Size: 1 << 40, MTime: time.Now(), Lib: "movies",
		Dest:   strings.Repeat("x", 200) + "/" + strings.Repeat("y", 200) + ".mkv",
		Status: jobs.StatusCopied, Kind: "episode", ResolvedBy: ByTMDB, TitleSource: TitleTMDBTranslation,
		Quality: 216061, DestIno: 1 << 62, At: time.Now(),
	}
	b, _ := json.Marshal(map[string]LedgerEntry{"18446744073709551615:18446744073709551615": e})
	if total := int64(len(b)) * maxLedgerEntries; total > state.MaxDocument {
		t.Errorf("a full ledger takes about %d bytes, more than state.MaxDocument (%d)", total, state.MaxDocument)
	}
}

// A full ledger drops its oldest entries in one batch, not one per Put, and
// updating an entry already in it evicts nothing.
func TestLedgerEvictsOldestInBatches(t *testing.T) {
	l := NewLedger()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < maxLedgerEntries; i++ {
		l.setLocked("1:"+strconv.Itoa(i), LedgerEntry{Lib: "movies", Dest: "d" + strconv.Itoa(i), At: base.Add(time.Duration(i) * time.Millisecond)})
	}
	src := filepath.Join(t.TempDir(), "a.mkv")
	write(t, src)
	fi, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	l.Put(fi, LedgerEntry{Lib: "movies", Dest: "a.mkv"})
	if got, want := len(l.m), maxLedgerEntries-ledgerEvictBatch+1; got != want {
		t.Fatalf("entries = %d, want %d", got, want)
	}
	for _, i := range []int{0, ledgerEvictBatch - 1} {
		if _, ok := l.m["1:"+strconv.Itoa(i)]; ok {
			t.Errorf("entry %d (among the oldest) kept", i)
		}
	}
	if _, ok := l.m["1:"+strconv.Itoa(ledgerEvictBatch)]; !ok {
		t.Error("the oldest entry after the batch was dropped")
	}
	if _, ok := l.ByDest("movies", "d"+strconv.Itoa(ledgerEvictBatch-1)); ok {
		t.Error("byDest still points at an evicted entry")
	}
	n := len(l.m)
	l.Put(fi, LedgerEntry{Lib: "movies", Dest: "b.mkv"})
	if len(l.m) != n {
		t.Errorf("updating an entry changed the size from %d to %d", n, len(l.m))
	}
}
