package jobs

import "testing"

func TestHistoryExportImport(t *testing.T) {
	h := NewHistory(3)
	changes := 0
	h.OnChange = func() { changes++ }
	for _, s := range []string{"a", "b", "c", "d"} {
		h.Add(Entry{Action: ActionOrganize, Source: s, Status: StatusLinked})
	}
	h.SetSync(SyncStatus{Status: StatusOK})
	if changes != 5 {
		t.Errorf("changes = %d, want 5", changes)
	}
	data, err := h.Export()
	if err != nil {
		t.Fatal(err)
	}
	r := NewHistory(2)
	if err := r.Import(data); err != nil {
		t.Fatal(err)
	}
	entries, last := r.Snapshot()
	if len(entries) != 2 || entries[0].Action != ActionSync || entries[1].Source != "d" || last == nil || last.Status != StatusOK {
		t.Errorf("restored = %+v %+v", entries, last)
	}
	r.Add(Entry{Source: "e"})
	if entries, _ := r.Snapshot(); entries[0].Source != "e" || entries[1].Action != ActionSync {
		t.Errorf("after add = %+v", entries)
	}
}
