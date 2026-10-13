package orphans

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jfxdev/media-maid/internal/config"
	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/organizer"
	"github.com/jfxdev/media-maid/internal/safepath"
)

type fakeServer struct {
	mu      sync.Mutex
	items   []integration.ServerItem
	deleted []string
}

func (f *fakeServer) ListItems(context.Context) ([]integration.ServerItem, error) {
	return f.items, nil
}

func (f *fakeServer) GetItem(_ context.Context, id string) (integration.ServerItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, it := range f.items {
		if it.ID == id {
			return it, nil
		}
	}
	return integration.ServerItem{}, integration.ErrNotFound
}

func (f *fakeServer) DeleteItem(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

type env struct {
	det     *Detector
	server  *fakeServer
	movies  string
	tv      string
	outside string
}

func write(t *testing.T, p string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T, dryRun bool, withServer bool) *env {
	vol := t.TempDir()
	e := &env{
		movies:  filepath.Join(vol, "media", "movies"),
		tv:      filepath.Join(vol, "media", "tv"),
		outside: t.TempDir(),
		server:  &fakeServer{},
	}
	os.MkdirAll(e.movies, 0o755)
	os.MkdirAll(e.tv, 0o755)
	g, _ := safepath.NewGuard([]string{vol})
	mr, _ := os.OpenRoot(e.movies)
	tr, _ := os.OpenRoot(e.tv)
	t.Cleanup(func() { mr.Close(); tr.Close() })
	var srv Server
	if withServer {
		srv = e.server
	}
	e.det = New(Options{
		Server:  srv,
		PathMap: &config.PathMap{Server: "/media", Local: filepath.Join(vol, "media")},
		Libraries: []organizer.Library{
			{Name: "movies", Path: e.movies, Root: mr},
			{Name: "tv", Path: e.tv, Root: tr},
		},
		Filter:  organizer.Filter{Video: map[string]bool{".mkv": true}},
		Guard:   g,
		History: jobs.NewHistory(50),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DryRun:  dryRun,
	})
	return e
}

func byType(r *Report) map[string][]Orphan {
	m := map[string][]Orphan{}
	for _, o := range r.Orphans {
		m[o.Type] = append(m[o.Type], o)
	}
	return m
}

func setupTree(t *testing.T, e *env) {
	write(t, filepath.Join(e.movies, "Kept (2000)", "Kept (2000).mkv"))
	write(t, filepath.Join(e.movies, "Kept (2000)", "Kept (2000).en.srt"))
	write(t, filepath.Join(e.movies, "Gone (2001)", "Gone (2001).pt.srt"))
	write(t, filepath.Join(e.movies, "Gone (2001)", "movie.nfo"))
	os.MkdirAll(filepath.Join(e.movies, "Empty (2002)"), 0o755)
	write(t, filepath.Join(e.tv, "Show", "Season 01", "Show S01E01.mkv"))
	os.MkdirAll(filepath.Join(e.tv, "Show", "Season 02"), 0o755)
	write(t, filepath.Join(e.outside, "real.mkv"))
	os.Symlink(e.outside, filepath.Join(e.movies, "Linked (2003)"))

	e.server.items = []integration.ServerItem{
		{ID: "1", Title: "Kept", Paths: []string{"/media/movies/Kept (2000)/Kept (2000).mkv"}},
		{ID: "2", Title: "Gone", Paths: []string{"/media/movies/Gone (2001)/Gone (2001).mkv"}},
		{ID: "3", Title: "Elsewhere", Paths: []string{"/music/song.mkv"}},
		{ID: "4", Title: "Linked", Paths: []string{"/media/movies/Linked (2003)/real.mkv"}},
		{ID: "5", Title: "Ep2", Paths: []string{"/media/tv/Show/Season 01/Show S01E02.mkv"}},
		// Multi-part item with one part outside the managed libraries: never an orphan.
		{ID: "6", Title: "Mixed", Paths: []string{"/media/movies/Mixed (2004)/part1.mkv", "/elsewhere/part2.mkv"}},
	}
}

func TestScan(t *testing.T) {
	e := newEnv(t, false, true)
	setupTree(t, e)
	rep, err := e.det.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.ServerCheck != "ok" {
		t.Errorf("server_check = %s", rep.ServerCheck)
	}
	got := byType(rep)

	var ids []string
	for _, o := range got[TypeServerItemMissingFile] {
		ids = append(ids, o.ServerItemID)
	}
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "2" || ids[1] != "5" {
		t.Errorf("missing-file items = %v, want [2 5]", ids)
	}

	var dirs []string
	for _, o := range got[TypeOrphanDirectory] {
		dirs = append(dirs, o.Path)
	}
	sort.Strings(dirs)
	want := []string{"movies/Empty (2002)", "movies/Gone (2001)", "tv/Show/Season 02"}
	if len(dirs) != len(want) {
		t.Fatalf("dirs = %v, want %v", dirs, want)
	}
	for i := range want {
		if dirs[i] != want[i] {
			t.Errorf("dirs = %v, want %v", dirs, want)
		}
	}

	if len(rep.Errors) != 1 || rep.Errors[0].ServerItemID != "4" || rep.Errors[0].Error != "unauthorized_volume" {
		t.Errorf("errors = %+v", rep.Errors)
	}
}

func TestScanWithoutServer(t *testing.T) {
	e := newEnv(t, false, false)
	setupTree(t, e)
	rep, _ := e.det.Scan(context.Background())
	if rep.ServerCheck != "disabled" || len(byType(rep)[TypeServerItemMissingFile]) != 0 {
		t.Errorf("report = %+v", rep)
	}
}

func idsOf(rep *Report, typ string) map[string]string {
	m := map[string]string{}
	for _, o := range rep.Orphans {
		if o.Type == typ {
			m[o.Path] = o.ID
		}
	}
	return m
}

func TestRemove(t *testing.T) {
	e := newEnv(t, false, true)
	setupTree(t, e)
	rep, _ := e.det.Scan(context.Background())
	dirs := idsOf(rep, TypeOrphanDirectory)
	var itemID string
	for _, o := range rep.Orphans {
		if o.ServerItemID == "2" {
			itemID = o.ID
		}
	}

	// A video reappears between scan and removal → stale, nothing deleted.
	write(t, filepath.Join(e.movies, "Empty (2002)", "Empty (2002).mkv"))

	res := e.det.Remove(context.Background(), []string{
		dirs["movies/Gone (2001)"], dirs["movies/Empty (2002)"], itemID, "deadbeef",
	})
	want := []string{jobs.StatusRemoved, jobs.StatusStale, jobs.StatusRemoved, jobs.StatusStale}
	for i, r := range res {
		if r.Status != want[i] {
			t.Errorf("result %d = %+v, want %s", i, r, want[i])
		}
	}
	if _, err := os.Stat(filepath.Join(e.movies, "Gone (2001)")); !os.IsNotExist(err) {
		t.Error("orphan directory not removed")
	}
	if _, err := os.Stat(filepath.Join(e.movies, "Empty (2002)", "Empty (2002).mkv")); err != nil {
		t.Error("revalidation failed: video removed")
	}
	if len(e.server.deleted) != 1 || e.server.deleted[0] != "2" {
		t.Errorf("deleted = %v", e.server.deleted)
	}
	if _, err := os.Stat(filepath.Join(e.outside, "real.mkv")); err != nil {
		t.Error("file outside library touched")
	}
	// Removed entries disappear from the report and cannot be removed twice.
	if r := e.det.Remove(context.Background(), []string{itemID}); r[0].Status != jobs.StatusStale {
		t.Errorf("second remove = %+v", r[0])
	}
}

func TestRemoveRevalidatesServerItem(t *testing.T) {
	e := newEnv(t, false, true)
	setupTree(t, e)
	rep, _ := e.det.Scan(context.Background())
	var id string
	for _, o := range rep.Orphans {
		if o.ServerItemID == "2" {
			id = o.ID
		}
	}
	write(t, filepath.Join(e.movies, "Gone (2001)", "Gone (2001).mkv")) // file restored
	if r := e.det.Remove(context.Background(), []string{id}); r[0].Status != jobs.StatusStale {
		t.Errorf("result = %+v", r[0])
	}
	if len(e.server.deleted) != 0 {
		t.Error("server item deleted although file exists again")
	}
}

func TestRemoveSymlinkSwap(t *testing.T) {
	e := newEnv(t, false, true)
	setupTree(t, e)
	rep, _ := e.det.Scan(context.Background())
	id := idsOf(rep, TypeOrphanDirectory)["movies/Gone (2001)"]
	// Replace the orphan directory by a symlink to an outside directory.
	os.RemoveAll(filepath.Join(e.movies, "Gone (2001)"))
	os.Symlink(e.outside, filepath.Join(e.movies, "Gone (2001)"))
	if r := e.det.Remove(context.Background(), []string{id}); r[0].Status != jobs.StatusStale {
		t.Errorf("result = %+v", r[0])
	}
	if _, err := os.Stat(filepath.Join(e.outside, "real.mkv")); err != nil {
		t.Error("followed symlink and deleted outside data")
	}
}

func TestRemoveDryRun(t *testing.T) {
	e := newEnv(t, true, true)
	setupTree(t, e)
	rep, _ := e.det.Scan(context.Background())
	var ids []string
	for _, o := range rep.Orphans {
		ids = append(ids, o.ID)
	}
	for _, r := range e.det.Remove(context.Background(), ids) {
		if r.Status != jobs.StatusDryRun {
			t.Errorf("result = %+v", r)
		}
	}
	if _, err := os.Stat(filepath.Join(e.movies, "Gone (2001)")); err != nil {
		t.Error("dry run removed a directory")
	}
	if len(e.server.deleted) != 0 {
		t.Error("dry run deleted a server item")
	}
}

func TestRemoveRejectsRoot(t *testing.T) {
	e := newEnv(t, false, true)
	o := Orphan{ID: "x", Type: TypeOrphanDirectory, library: "movies", rel: "."}
	if r := e.det.removeOne(context.Background(), o); r.Status != jobs.StatusError {
		t.Errorf("root removal = %+v", r)
	}
	o.rel = "../tv"
	if r := e.det.removeOne(context.Background(), o); r.Status != jobs.StatusError {
		t.Errorf("parent removal = %+v", r)
	}
	if _, err := os.Stat(e.tv); err != nil {
		t.Error("tv library removed")
	}
}

func TestUnlinkedDownloads(t *testing.T) {
	vol := t.TempDir()
	downloads := filepath.Join(vol, "downloads")
	movies := filepath.Join(vol, "movies")
	os.MkdirAll(movies, 0o755)
	linked := filepath.Join(downloads, "Linked.2001", "Linked.2001.mkv")
	unlinked := filepath.Join(downloads, "Gone.2002", "Gone.2002.mkv")
	copied := filepath.Join(downloads, "Copied.2003.mkv")
	for _, p := range []string{linked, unlinked, copied, filepath.Join(downloads, "Gone.2002", "Sample", "sample.mkv"), filepath.Join(downloads, "notes.txt")} {
		write(t, p)
	}
	os.MkdirAll(filepath.Join(movies, "Linked (2001)"), 0o755)
	if err := os.Link(linked, filepath.Join(movies, "Linked (2001)", "Linked (2001).mkv")); err != nil {
		t.Fatal(err)
	}
	// A copy keeps the size and mtime of its source.
	fi, _ := os.Stat(copied)
	libCopy := filepath.Join(movies, "Copied (2003)", "Copied (2003).mkv")
	os.MkdirAll(filepath.Dir(libCopy), 0o755)
	data, _ := os.ReadFile(copied)
	os.WriteFile(libCopy, data, 0o644)
	os.Chtimes(libCopy, fi.ModTime(), fi.ModTime())
	ledger := organizer.NewLedger()
	ledger.Put(fi, organizer.LedgerEntry{Lib: "movies", Dest: "Copied (2003)/Copied (2003).mkv", Status: jobs.StatusCopied})
	// A copy since replaced by another release (an upgrade) no longer counts.
	replaced := filepath.Join(downloads, "Replaced.2004.mkv")
	write(t, replaced)
	write(t, filepath.Join(movies, "Replaced (2004)", "Replaced (2004).mkv"))
	rfi, _ := os.Stat(replaced)
	ledger.Put(rfi, organizer.LedgerEntry{Lib: "movies", Dest: "Replaced (2004)/Replaced (2004).mkv", Status: jobs.StatusCopied})
	// Same size and mtime, but not the library file recorded for the copy.
	swapped := filepath.Join(downloads, "Swapped.2005.mkv")
	write(t, swapped)
	sfi, _ := os.Stat(swapped)
	libSwap := filepath.Join(movies, "Swapped (2005)", "Swapped (2005).mkv")
	os.MkdirAll(filepath.Dir(libSwap), 0o755)
	sdata, _ := os.ReadFile(swapped)
	os.WriteFile(libSwap, sdata, 0o644)
	os.Chtimes(libSwap, sfi.ModTime(), sfi.ModTime())
	lfi, _ := os.Stat(libSwap)
	ledger.Put(sfi, organizer.LedgerEntry{Lib: "movies", Dest: "Swapped (2005)/Swapped (2005).mkv", Status: jobs.StatusCopied, DestIno: organizer.Inode(lfi) + 1})

	// Linked elsewhere too (a cross-seed folder), but its library link was
	// deleted: still not in the library.
	seeded := filepath.Join(downloads, "Seeded.2006.mkv")
	write(t, seeded)
	os.MkdirAll(filepath.Join(vol, "cross-seed"), 0o755)
	if err := os.Link(seeded, filepath.Join(vol, "cross-seed", "Seeded.2006.mkv")); err != nil {
		t.Fatal(err)
	}
	seededFi, _ := os.Stat(seeded)
	ledger.Put(seededFi, organizer.LedgerEntry{Lib: "movies", Dest: "Seeded (2006)/Seeded (2006).mkv", Status: jobs.StatusLinked})
	// Linked elsewhere and never organized by Media Maid: doubt, not reported.
	foreign := filepath.Join(downloads, "Foreign.2007.mkv")
	write(t, foreign)
	if err := os.Link(foreign, filepath.Join(vol, "cross-seed", "Foreign.2007.mkv")); err != nil {
		t.Fatal(err)
	}

	mr, _ := os.OpenRoot(movies)
	defer mr.Close()
	g, _ := safepath.NewGuard([]string{vol})
	det := New(Options{
		Libraries: []organizer.Library{{Name: "movies", Path: movies, Root: mr}},
		Filter:    organizer.Filter{Video: map[string]bool{".mkv": true}},
		Guard:     g, History: jobs.NewHistory(10), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Downloads: downloads, Ledger: ledger,
	})
	rep, err := det.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var id string
	for _, o := range rep.Orphans {
		if o.Type == TypeUnlinkedDownload {
			got = append(got, o.Path)
			if id == "" {
				id = o.ID
			}
		}
	}
	sort.Strings(got)
	want := []string{"downloads/Gone.2002/Gone.2002.mkv", "downloads/Replaced.2004.mkv", "downloads/Seeded.2006.mkv", "downloads/Swapped.2005.mkv"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unlinked = %v (report %+v)", got, rep)
	}
	if res := det.Remove(context.Background(), []string{id}); res[0].Status != jobs.StatusError || res[0].Error != errNotRemovable {
		t.Errorf("remove = %+v", res)
	}
	if _, err := os.Stat(unlinked); err != nil {
		t.Error("a download was removed")
	}
}

func TestReportPersistence(t *testing.T) {
	e := newEnv(t, false, false)
	write(t, filepath.Join(e.movies, "Empty (2000)", "poster.jpg"))
	rep, _ := e.det.Scan(context.Background())
	if len(rep.Orphans) != 1 {
		t.Fatalf("orphans = %+v", rep.Orphans)
	}
	data, err := e.det.Export()
	if err != nil {
		t.Fatal(err)
	}
	restored := New(Options{Libraries: e.det.libraries, Filter: e.det.filter, Guard: e.det.guard, History: jobs.NewHistory(10), Log: e.det.log})
	if err := restored.Import(data); err != nil {
		t.Fatal(err)
	}
	if r := restored.Report(); r == nil || len(r.Orphans) != 1 {
		t.Fatalf("restored report = %+v", r)
	}
	// Removal after a restart still works, with revalidation.
	if res := restored.Remove(context.Background(), []string{rep.Orphans[0].ID}); res[0].Status != jobs.StatusRemoved {
		t.Errorf("remove after restore = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(e.movies, "Empty (2000)")); err == nil {
		t.Error("directory not removed")
	}
}

// Only folders holding nothing but metadata are orphans: an ISO, a Blu-ray
// stream or a .strm keeps the folder, in the report and on removal. Removal
// runs under the organizer's exclusive lock.
func TestOrphanDirsKeepContent(t *testing.T) {
	e := newEnv(t, false, false)
	var locked int
	e.det.exclusive = func(f func()) { locked++; f() }
	for _, p := range []string{
		filepath.Join(e.movies, "Disc (2000)", "Disc (2000).iso"),
		filepath.Join(e.movies, "BD (2001)", "BDMV", "STREAM", "00001.m2ts"),
		filepath.Join(e.movies, "Stream (2002)", "Stream (2002).strm"),
		filepath.Join(e.movies, "Meta (2003)", "poster.jpg"),
		filepath.Join(e.movies, "Meta (2003)", "movie.nfo"),
		filepath.Join(e.movies, "Meta (2003)", "Meta (2003).pt-BR.srt"),
		filepath.Join(e.movies, "Meta (2003)", "Thumbs.db"),
	} {
		write(t, p)
	}
	rep, err := e.det.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, o := range byType(rep)[TypeOrphanDirectory] {
		dirs = append(dirs, o.Path)
	}
	if len(dirs) != 1 || dirs[0] != "movies/Meta (2003)" {
		t.Fatalf("orphan dirs = %v", dirs)
	}
	id := byType(rep)[TypeOrphanDirectory][0].ID

	// Content added after the report keeps the folder.
	write(t, filepath.Join(e.movies, "Meta (2003)", "Meta (2003).iso"))
	if res := e.det.Remove(context.Background(), []string{id}); res[0].Status != jobs.StatusStale {
		t.Errorf("remove with content = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(e.movies, "Meta (2003)", "Meta (2003).iso")); err != nil {
		t.Error("content removed")
	}
	if locked != 1 {
		t.Errorf("exclusive lock taken %d times, want 1", locked)
	}
}

// Removal checks the item as the server has it now, not the report's paths:
// an item that moved outside the managed libraries, got its file back, or
// disappeared is stale and never deleted.
func TestRemoveServerItemRefetches(t *testing.T) {
	e := newEnv(t, false, true)
	e.server.items = []integration.ServerItem{
		{ID: "1", Title: "Moved", Paths: []string{"/media/movies/Moved (2000)/Moved (2000).mkv"}},
		{ID: "2", Title: "Back", Paths: []string{"/media/movies/Back (2001)/Back (2001).mkv"}},
		{ID: "3", Title: "Gone", Paths: []string{"/media/movies/Gone (2002)/Gone (2002).mkv"}},
		{ID: "4", Title: "Still", Paths: []string{"/media/movies/Still (2003)/Still (2003).mkv"}},
	}
	rep, err := e.det.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, o := range byType(rep)[TypeServerItemMissingFile] {
		ids[o.ServerItemID] = o.ID
	}
	if len(ids) != 4 {
		t.Fatalf("orphans = %+v", rep.Orphans)
	}

	e.server.mu.Lock()
	e.server.items = []integration.ServerItem{
		{ID: "1", Title: "Moved", Paths: []string{"/elsewhere/Moved (2000).mkv"}},
		{ID: "2", Title: "Back", Paths: []string{"/media/movies/Back (2001)/Back (2001).mkv"}},
		{ID: "4", Title: "Still", Paths: []string{"/media/movies/Still (2003)/Still (2003).mkv"}},
	}
	e.server.mu.Unlock()
	write(t, filepath.Join(e.movies, "Back (2001)", "Back (2001).mkv"))

	res := e.det.Remove(context.Background(), []string{ids["1"], ids["2"], ids["3"], ids["4"]})
	want := []string{jobs.StatusStale, jobs.StatusStale, jobs.StatusStale, jobs.StatusRemoved}
	for i, r := range res {
		if r.Status != want[i] {
			t.Errorf("result %d = %+v, want %s", i, r, want[i])
		}
	}
	if len(e.server.deleted) != 1 || e.server.deleted[0] != "4" {
		t.Errorf("deleted = %v, want [4]", e.server.deleted)
	}
}
