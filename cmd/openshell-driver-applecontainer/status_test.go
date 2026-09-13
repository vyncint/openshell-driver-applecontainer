package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "driver.log")
	var content strings.Builder
	for i := 1; i <= 100; i++ {
		content.WriteString("line ")
		content.WriteString(strings.Repeat("x", i%7))
		content.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var out bytes.Buffer
	offset, err := tailLines(f, &out, 3)
	if err != nil {
		t.Fatal(err)
	}
	if offset != int64(content.Len()) {
		t.Errorf("offset = %d, want file size %d", offset, content.Len())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines: %q", len(lines), out.String())
	}
	want := strings.Split(strings.TrimRight(content.String(), "\n"), "\n")
	if lines[2] != want[99] || lines[0] != want[97] {
		t.Errorf("tail = %q, want last three of the file", lines)
	}

	// Asking for more lines than exist returns the whole file, once.
	out.Reset()
	if _, err := tailLines(f, &out, 1000); err != nil {
		t.Fatal(err)
	}
	if out.String() != content.String() {
		t.Errorf("full tail differs from file (%d vs %d bytes)", out.Len(), content.Len())
	}

	// An empty file prints nothing and does not error.
	empty, err := os.Create(filepath.Join(t.TempDir(), "empty.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = empty.Close() }()
	out.Reset()
	if off, err := tailLines(empty, &out, 5); err != nil || off != 0 || out.Len() != 0 {
		t.Errorf("empty file: off=%d err=%v out=%q", off, err, out.String())
	}
}
