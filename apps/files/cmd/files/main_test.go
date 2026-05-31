package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanAnyPathRequiresAbsolutePath(t *testing.T) {
	if _, err := cleanAnyPath("relative/file"); err == nil {
		t.Fatalf("expected relative path to be rejected")
	}
	got, err := cleanAnyPath("/tmp/../tmp/file")
	if err != nil {
		t.Fatalf("clean absolute path: %v", err)
	}
	if got != "/tmp/file" {
		t.Fatalf("clean path = %q", got)
	}
}

func TestDeleteRejectsNonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := newFilesApp(dir, "")
	req := httptestRequestJSON(writePathRequest{Path: dir})
	rec := httptestRecorder()
	app.deleteFS(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunHelperRejectsUnsupportedAction(t *testing.T) {
	var out bytes.Buffer
	err := runHelper(strings.NewReader(`{"action":"format"}`), &out)
	if err == nil {
		t.Fatalf("expected unsupported helper action error")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountIDStable(t *testing.T) {
	a := mountID("/tmp/disk.qcow2")
	b := mountID("/tmp/../tmp/disk.qcow2")
	if a != b {
		t.Fatalf("mountID should use clean paths: %q != %q", a, b)
	}
}

func TestListDirectorySortsDirectoriesFirst(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "z-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := listDirectory(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "z-dir" || items[0].Type != "dir" {
		t.Fatalf("unexpected order: %#v", items)
	}
}

func TestQemuInfoJSONShape(t *testing.T) {
	raw := []byte(`{"filename":"disk.qcow2","format":"qcow2","virtual-size":1073741824,"actual-size":196616,"backing-filename":"base.qcow2"}`)
	var info qemuInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	if info.Format != "qcow2" || info.VirtualSize != 1073741824 || info.BackingFilename != "base.qcow2" {
		t.Fatalf("unexpected info: %#v", info)
	}
}

func TestQemuInfoCommandUsesForceShare(t *testing.T) {
	cmd := qemuImgInfoCommand(context.Background(), "/tmp/disk.qcow2")
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, " -U ") {
		t.Fatalf("qemu-img info must use -U for running VM disks, got %q", args)
	}
}

func TestMountListCopiesSessions(t *testing.T) {
	app := newFilesApp(t.TempDir(), "")
	app.mounts["id"] = &mountSession{ID: "id", Image: "/tmp/a.qcow2", Path: "/tmp/m", LastUsed: time.Now()}
	items := app.mountList()
	items[0].Image = "changed"
	if app.mounts["id"].Image == "changed" {
		t.Fatalf("mountList leaked mutable session")
	}
}

func TestMountBackend(t *testing.T) {
	if got := mountBackend(helperResponse{Backend: "guestmount"}); got != "guestmount" {
		t.Fatalf("mountBackend = %q", got)
	}
	if got := mountBackend(helperResponse{NBD: "/dev/nbd0"}); got != "qemu-nbd" {
		t.Fatalf("mountBackend fallback = %q", got)
	}
}

func TestChooseMountSourceFromLSBLKPrefersFilesystemPartition(t *testing.T) {
	source, ok := chooseMountSourceFromLSBLK([]byte(`{
  "blockdevices": [
    {
      "name": "/dev/nbd0",
      "type": "disk",
      "fstype": null,
      "children": [
        {"name": "/dev/nbd0p1", "type": "part", "fstype": "vfat"},
        {"name": "/dev/nbd0p2", "type": "part", "fstype": "ext4"},
        {"name": "/dev/nbd0p3", "type": "part", "fstype": "swap"}
      ]
    }
  ]
}`))
	if !ok || source != "/dev/nbd0p2" {
		t.Fatalf("source = %q ok=%v", source, ok)
	}
}

func TestChooseMountSourceFromLSBLKAcceptsUnpartitionedFilesystem(t *testing.T) {
	source, ok := chooseMountSourceFromLSBLK([]byte(`{
  "blockdevices": [
    {"name": "/dev/nbd0", "type": "disk", "fstype": "ext4"}
  ]
}`))
	if !ok || source != "/dev/nbd0" {
		t.Fatalf("source = %q ok=%v", source, ok)
	}
}

func TestChooseMountSourceFromLSBLKRejectsEmptyDisk(t *testing.T) {
	source, ok := chooseMountSourceFromLSBLK([]byte(`{
  "blockdevices": [
    {"name": "/dev/nbd0", "type": "disk", "fstype": null}
  ]
}`))
	if ok || source != "" {
		t.Fatalf("source = %q ok=%v", source, ok)
	}
}

func TestInsideRuntimeMountRootAcceptsTmpFallback(t *testing.T) {
	path := filepath.Join(os.TempDir(), "foxlab", "files", "mounts", "abc")
	if !insideRuntimeMountRoot(path) {
		t.Fatalf("expected %s to be accepted", path)
	}
	if insideRuntimeMountRoot(filepath.Join(os.TempDir(), "foxlab", "files", "other")) {
		t.Fatalf("unexpected path accepted")
	}
}

func TestLibvirtVolumeMetadataParsesBackingStore(t *testing.T) {
	info, layers, err := libvirtVolumeMetadata("/images/overlay.qcow2", `<volume>
  <name>overlay.qcow2</name>
  <capacity unit="bytes">1073741824</capacity>
  <allocation unit="bytes">196616</allocation>
  <target>
    <path>/images/overlay.qcow2</path>
    <format type="qcow2"></format>
  </target>
  <backingStore>
    <path>/images/base.qcow2</path>
    <format type="qcow2"></format>
  </backingStore>
</volume>`)
	if err != nil {
		t.Fatal(err)
	}
	if info.Filename != "/images/overlay.qcow2" || info.Format != "qcow2" {
		t.Fatalf("unexpected info: %#v", info)
	}
	if info.VirtualSize != 1073741824 || info.ActualSize != 196616 {
		t.Fatalf("unexpected size info: %#v", info)
	}
	if info.FullBackingFilename != "/images/base.qcow2" {
		t.Fatalf("unexpected backing file: %#v", info)
	}
	if len(layers) != 2 || layers[0].Path != "/images/overlay.qcow2" || layers[1].Path != "/images/base.qcow2" {
		t.Fatalf("unexpected layers: %#v", layers)
	}
}

func TestParseDomainDiskPaths(t *testing.T) {
	paths := parseDomainDiskPaths(`<domain type="kvm">
  <devices>
    <disk type="file" device="disk">
      <source file="/var/lib/libvirt/images/vm.qcow2"></source>
    </disk>
    <disk type="block" device="disk">
      <source dev="/dev/vg0/vm-root"></source>
    </disk>
    <disk type="file" device="cdrom">
      <source file="/isos/alpine.iso"></source>
    </disk>
  </devices>
</domain>`)
	if len(paths) != 2 {
		t.Fatalf("unexpected paths: %#v", paths)
	}
	if paths[0] != "/var/lib/libvirt/images/vm.qcow2" || paths[1] != "/dev/vg0/vm-root" {
		t.Fatalf("unexpected paths: %#v", paths)
	}
}

func httptestRequestJSON(v any) *http.Request {
	var body bytes.Buffer
	_ = json.NewEncoder(&body).Encode(v)
	req := httptest.NewRequest(http.MethodPost, "/", &body)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func httptestRecorder() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}
