// Package state persists accepted sandbox launch records so the driver can
// reconcile against the container runtime after a restart. The runtime, not
// these records, is the source of truth for liveness; records carry the
// accepted spec and identity.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// ErrNotFound is returned when no record exists for the requested sandbox.
var ErrNotFound = errors.New("state: record not found")

// idPattern mirrors the upstream driver's sandbox-id validation; it doubles
// as path-traversal protection because the id becomes a directory name.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// ValidID reports whether id is safe to use as a sandbox identifier and
// state directory name.
func ValidID(id string) bool {
	return idPattern.MatchString(id) && id != "." && id != ".."
}

// Record is one accepted sandbox launch. Sandbox holds the protojson
// encoding of the accepted DriverSandbox exactly as received, so restart
// reconciliation can rebuild observations without loss.
type Record struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Namespace     string          `json:"namespace,omitempty"`
	Workspace     string          `json:"workspace,omitempty"`
	ContainerName string          `json:"container_name"`
	ImageRef      string          `json:"image_ref"`
	ImageDigest   string          `json:"image_digest,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	Sandbox       json.RawMessage `json:"sandbox,omitempty"`
}

// Store reads and writes per-sandbox record files under
// <root>/sandboxes/<id>/state.json with owner-only permissions.
type Store struct {
	root string
}

// NewStore creates the store layout under root (mode 0700).
func NewStore(root string) (*Store, error) {
	s := &Store{root: root}
	if err := os.MkdirAll(s.sandboxesDir(), 0o700); err != nil {
		return nil, fmt.Errorf("state: create store: %w", err)
	}
	return s, nil
}

func (s *Store) sandboxesDir() string {
	return filepath.Join(s.root, "sandboxes")
}

// SandboxDir returns the per-sandbox state directory (created by Save).
func (s *Store) SandboxDir(id string) string {
	return filepath.Join(s.sandboxesDir(), id)
}

func (s *Store) recordPath(id string) string {
	return filepath.Join(s.SandboxDir(id), "state.json")
}

// Save writes the record atomically (temp file + rename) before any VM is
// booted for it.
func (s *Store) Save(rec Record) error {
	if !ValidID(rec.ID) {
		return fmt.Errorf("state: invalid sandbox id %q", rec.ID)
	}
	dir := s.SandboxDir(rec.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state: create sandbox dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encode record: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("state: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: write record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close record: %w", err)
	}
	if err := os.Rename(tmpName, s.recordPath(rec.ID)); err != nil {
		return fmt.Errorf("state: persist record: %w", err)
	}
	return nil
}

// Load reads one record by sandbox id.
func (s *Store) Load(id string) (Record, error) {
	if !ValidID(id) {
		return Record{}, fmt.Errorf("state: invalid sandbox id %q", id)
	}
	data, err := os.ReadFile(s.recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Record{}, fmt.Errorf("state: read record: %w", err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("state: decode record %s: %w", id, err)
	}
	return rec, nil
}

// List returns all readable records. Directories without a parseable
// state.json are skipped and reported in skipped (they are never deleted
// here; cleanup is an explicit reconcile decision).
func (s *Store) List() (records []Record, skipped []string, err error) {
	entries, err := os.ReadDir(s.sandboxesDir())
	if err != nil {
		return nil, nil, fmt.Errorf("state: scan store: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			skipped = append(skipped, e.Name())
			continue
		}
		rec, lerr := s.Load(e.Name())
		if lerr != nil {
			skipped = append(skipped, e.Name())
			continue
		}
		if rec.ID != e.Name() {
			// Directory name and record identity must agree.
			skipped = append(skipped, e.Name())
			continue
		}
		records = append(records, rec)
	}
	return records, skipped, nil
}

// Delete removes the sandbox's state directory (record, seed material).
func (s *Store) Delete(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("state: invalid sandbox id %q", id)
	}
	if err := os.RemoveAll(s.SandboxDir(id)); err != nil {
		return fmt.Errorf("state: delete record: %w", err)
	}
	return nil
}
