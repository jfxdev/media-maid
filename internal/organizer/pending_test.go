package organizer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jfxdev/media-maid/internal/jobs"
)

// A problem is pending (and told) once, however many scans see it, and goes
// away when it is solved or its file leaves the downloads folder.
func TestPendingAndNotify(t *testing.T) {
	var told []Result
	e := newEnv(t, "jellyfin", func(o *Options) { o.Notify = func(r Result) { told = append(told, r) } })
	foreign := filepath.Join(e.movies, "The Matrix (1999)", "The Matrix (1999).mkv")
	write(t, foreign)
	write(t, filepath.Join(e.downloads, "The.Matrix.1999.1080p.mkv"))
	write(t, filepath.Join(e.downloads, "a1b2c3d4e5f60718.mkv"))
	write(t, filepath.Join(e.downloads, "Inception.2010.mkv"))

	for i := 0; i < 3; i++ {
		e.org.Scan(context.Background())
	}
	items := e.org.Pending()
	if len(items) != 2 {
		t.Fatalf("pending = %+v", items)
	}
	byStatus := map[string]PendingItem{}
	for _, it := range items {
		byStatus[it.Status] = it
	}
	if byStatus[jobs.StatusConflict].Source != "The.Matrix.1999.1080p.mkv" || byStatus[jobs.StatusSkipped].Source != "a1b2c3d4e5f60718.mkv" {
		t.Errorf("pending = %+v", items)
	}
	// Told once each, plus the linked movie.
	if len(told) != 3 {
		t.Errorf("notifications = %+v", told)
	}

	// Solved by an explicit overwrite.
	if _, err := e.org.Organize(context.Background(), "The.Matrix.1999.1080p.mkv", OrganizeOptions{Explicit: true, Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	// Gone from the downloads folder.
	os.Remove(filepath.Join(e.downloads, "a1b2c3d4e5f60718.mkv"))
	e.org.Scan(context.Background())
	if items := e.org.Pending(); len(items) != 0 {
		t.Errorf("pending after fixes = %+v", items)
	}

	// The list survives a restart.
	p := NewPending()
	p.Update(Result{Source: "x.mkv", Status: jobs.StatusError, Error: "boom"})
	data, _ := p.Export()
	q := NewPending()
	if err := q.Import(data); err != nil || len(q.List()) != 1 || q.List()[0].Error != "boom" {
		t.Errorf("import = %+v, %v", q.List(), err)
	}
}

// Previews and dry runs never touch the pending list.
func TestPendingIgnoresPreview(t *testing.T) {
	e := newEnv(t, "jellyfin")
	write(t, filepath.Join(e.downloads, "a1b2c3d4e5f60718.mkv"))
	e.org.Organize(context.Background(), e.downloads, OrganizeOptions{Preview: true})
	if items := e.org.Pending(); len(items) != 0 {
		t.Errorf("pending = %+v", items)
	}
}
