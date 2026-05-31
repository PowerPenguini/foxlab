package foxapp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPackagesFindsFoxAppsInAppDirs(t *testing.T) {
	srcDir := t.TempDir()
	writeFile(t, filepath.Join(srcDir, ManifestFile), `{
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
	writeFile(t, filepath.Join(srcDir, "bin", "topology"), "binary")
	writeFile(t, filepath.Join(srcDir, "web", "dist", "index.html"), "<!doctype html>")

	appDir := t.TempDir()
	if err := Package(srcDir, filepath.Join(appDir, "topology.foxapp")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "broken.foxapp"), []byte("not a package"), 0o644); err != nil {
		t.Fatal(err)
	}

	refs, err := DiscoverPackages([]string{appDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("got %d discovered package(s), want 1", len(refs))
	}
	if refs[0].Manifest.ID != "topology" {
		t.Fatalf("unexpected app id %q", refs[0].Manifest.ID)
	}
}
