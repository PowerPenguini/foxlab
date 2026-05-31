package foxapp

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageCreatesFoxAppArchive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ManifestFile), `{
  "format": "foxapp.v1",
  "id": "topology",
  "name": "Topology Editor",
  "version": "0.1.0",
  "run": {"command": "bin/topology"},
  "icon": {"type": "builtin", "value": "network"},
  "window": {"title": "Topology editor", "path": "/"},
  "health": {"path": "/healthz"},
  "web": {"dist": "web/dist"}
}`)
	writeFile(t, filepath.Join(dir, "bin", "topology"), "binary")
	writeFile(t, filepath.Join(dir, "web", "dist", "index.html"), "<!doctype html>")

	out := filepath.Join(t.TempDir(), "topology.foxapp")
	if err := Package(dir, out); err != nil {
		t.Fatal(err)
	}
	manifest, err := Inspect(out)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "topology" {
		t.Fatalf("unexpected app id %q", manifest.ID)
	}

	entries := archiveEntries(t, out)
	for _, want := range []string{ManifestFile, "bin/topology", "web/dist/index.html"} {
		if !entries[want] {
			t.Fatalf("archive missing %s; entries=%v", want, entries)
		}
	}

	extractDir := t.TempDir()
	if err := Extract(out, extractDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(extractDir, "bin", "topology")); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
		t.Fatal(err)
	}
}

func archiveEntries(t *testing.T, path string) map[string]bool {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := map[string]bool{}
	for {
		header, err := tr.Next()
		if err != nil {
			break
		}
		entries[header.Name] = true
	}
	return entries
}
