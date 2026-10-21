package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A directory expands to its .pmtiles files by name, without the output or
// other files; file arguments pass through.
func TestExpandInputs(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b.pmtiles", "a.pmtiles", "out.pmtiles", "a.pmtiles.provenance.json"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := expandInputs([]string{dir, "x.pmtiles"}, filepath.Join(dir, "out.pmtiles"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "a.pmtiles"), filepath.Join(dir, "b.pmtiles"), "x.pmtiles"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := expandInputs([]string{t.TempDir()}, "o.pmtiles"); err == nil {
		t.Error("empty directory: no error")
	}
}
