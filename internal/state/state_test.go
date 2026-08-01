package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	rec := Record{
		ID:            "0195c1a2-dead-beef-0000-000000000001",
		Name:          "sb-one",
		Namespace:     "default",
		Workspace:     "ws1",
		ContainerName: "oshl-0195c1a2",
		ImageRef:      "ghcr.io/example/sandbox:1",
		ImageDigest:   "sha256:abc",
		CreatedAt:     time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		Sandbox:       json.RawMessage(`{"id":"0195c1a2-dead-beef-0000-000000000001"}`),
	}
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != rec.Name || got.ContainerName != rec.ContainerName || got.ImageDigest != rec.ImageDigest {
		t.Errorf("round trip mismatch: %+v", got)
	}
	var wantSb, gotSb map[string]any
	if err := json.Unmarshal(rec.Sandbox, &wantSb); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got.Sandbox, &gotSb); err != nil {
		t.Fatal(err)
	}
	if wantSb["id"] != gotSb["id"] {
		t.Errorf("sandbox payload mismatch: %s", got.Sandbox)
	}

	info, err := os.Stat(filepath.Join(s.SandboxDir(rec.ID), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("record perms = %o, want 600", perm)
	}
}

func TestLoadMissing(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Load("no-such-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestInvalidIDs(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"", "..", "a/b", "a b", "x\x00y", string(make([]byte, 200))} {
		if err := s.Save(Record{ID: id}); err == nil {
			t.Errorf("Save accepted invalid id %q", id)
		}
		if _, err := s.Load(id); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Load(%q) should fail validation, got %v", id, err)
		}
		if err := s.Delete(id); err == nil {
			t.Errorf("Delete accepted invalid id %q", id)
		}
	}
}

func TestListSkipsCorruptAndForeign(t *testing.T) {
	s := newTestStore(t)
	good := Record{ID: "good-1", Name: "g", ContainerName: "oshl-good-1", ImageRef: "img", CreatedAt: time.Now().UTC()}
	if err := s.Save(good); err != nil {
		t.Fatal(err)
	}

	// Corrupt record: unparseable JSON.
	if err := os.MkdirAll(s.SandboxDir("corrupt-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.SandboxDir("corrupt-1"), "state.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Identity mismatch: dir name != record id.
	if err := os.MkdirAll(s.SandboxDir("mismatch-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.SandboxDir("mismatch-1"), "state.json"), []byte(`{"id":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Empty dir (no state.json at all).
	if err := os.MkdirAll(s.SandboxDir("empty-1"), 0o700); err != nil {
		t.Fatal(err)
	}

	records, skipped, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "good-1" {
		t.Errorf("records = %+v", records)
	}
	if len(skipped) != 3 {
		t.Errorf("skipped = %v, want 3 entries", skipped)
	}
}

func TestDeleteRemovesDir(t *testing.T) {
	s := newTestStore(t)
	rec := Record{ID: "gone-1", ContainerName: "oshl-gone-1"}
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.SandboxDir(rec.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sandbox dir still present: %v", err)
	}
	// Deleting again is fine (idempotent).
	if err := s.Delete(rec.ID); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	s := newTestStore(t)
	rec := Record{ID: "atomic-1", ContainerName: "c"}
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	// Overwrite with new content; no stray temp files left behind.
	rec.Name = "updated"
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(s.SandboxDir(rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("leftover files: %v", names)
	}
	got, err := s.Load(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "updated" {
		t.Errorf("overwrite not visible: %+v", got)
	}
}
