package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"foxlab/internal/foxapp"
	"foxlab/internal/lab"
	"foxlab/internal/virt"
	"foxlab/internal/wm"
)

func TestShellAndTopologyAppsExposeSeparateRoutes(t *testing.T) {
	chdirRepoRoot(t)
	workspace := t.TempDir()
	appDir := t.TempDir()
	packageTestApp(t, appDir)

	shell := NewShell(Config{Workspace: workspace, AppDirs: []string{appDir}})
	topology := NewTopology(Config{Workspace: workspace})

	assertStatus(t, shell, http.MethodGet, "/", http.StatusOK)
	assertBodyContains(t, shell, http.MethodGet, "/", "Foxlab")
	assertStatus(t, shell, http.MethodGet, "/api/apps", http.StatusOK)
	assertBodyContains(t, shell, http.MethodGet, "/api/apps", `"id":"topology"`)
	assertStatus(t, shell, http.MethodGet, "/api/apps/topology", http.StatusOK)
	assertBodyContains(t, shell, http.MethodGet, "/api/apps/topology", `"name":"Topology Editor"`)
	assertBodyContains(t, shell, http.MethodGet, "/api/apps/topology", `"windowTitle":"Topology editor"`)
	assertBodyContains(t, shell, http.MethodGet, "/api/apps/topology", `"icon":{"type":"builtin","value":"network"}`)
	assertStatus(t, shell, http.MethodGet, "/api/labs", http.StatusNotFound)

	assertStatus(t, topology, http.MethodGet, "/", http.StatusOK)
	assertBodyContains(t, topology, http.MethodGet, "/", "Topology Editor")
	assertStatus(t, topology, http.MethodGet, "/healthz", http.StatusOK)
	assertStatus(t, topology, http.MethodGet, "/api/labs", http.StatusOK)
	assertStatus(t, topology, http.MethodGet, "/api/apps/topology", http.StatusNotFound)
}

func TestShellDesktopListsWorkspaceDirectory(t *testing.T) {
	chdirRepoRoot(t)
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "labs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(workspace, "vm1.qcow2"), "disk")
	writeTestFile(t, filepath.Join(workspace, "notes.txt"), "notes")

	shell := NewShell(Config{Workspace: workspace})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop", nil)
	shell.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/desktop returned %d: %s", rec.Code, rec.Body.String())
	}
	var listing desktopListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Path != workspace {
		t.Fatalf("path = %q, want %q", listing.Path, workspace)
	}
	if len(listing.Entries) != 3 {
		t.Fatalf("unexpected entries: %+v", listing.Entries)
	}
	if listing.Entries[0].Name != "labs" || listing.Entries[0].Type != "dir" {
		t.Fatalf("directories should sort first: %+v", listing.Entries)
	}
	if listing.Entries[1].Name != "notes.txt" || listing.Entries[2].Name != "vm1.qcow2" {
		t.Fatalf("files should sort by name: %+v", listing.Entries)
	}
}

func TestShellDesktopRejectsPathOutsideWorkspace(t *testing.T) {
	chdirRepoRoot(t)
	shell := NewShell(Config{Workspace: t.TempDir()})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop?path=/", nil)
	shell.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET /api/desktop outside workspace returned %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTopologyLabDocumentEndpointPersistsDeclarativeConfig(t *testing.T) {
	chdirRepoRoot(t)
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "demo.yaml"), `id: demo
name: Demo
`)
	topology := NewTopology(Config{Workspace: workspace})

	next := lab.Lab{
		ID:   "demo",
		Name: "Demo declarative",
		VMs: []lab.VM{{
			ID:       "vm1",
			Name:     "Alpine",
			MemoryMB: 2048,
			CPUs:     2,
			Disk:     "labs/demo/disks/vm1.qcow2",
			ISO:      "/home/user/Downloads/alpine.iso",
			VNC:      true,
			Networks: []lab.VMNetwork{{Switch: "sw1"}},
		}},
		Switches:      []lab.Switch{{ID: "sw1", Mode: "bridge", ExternalLink: "uplink1"}},
		ExternalLinks: []lab.ExternalLink{{ID: "uplink1", Name: "LAN", Interface: "br0"}},
		Disks: []lab.Disk{{
			ID:     "vm1",
			Path:   "labs/demo/disks/vm1.qcow2",
			SizeGB: 30,
			Format: "qcow2",
		}},
		Layout: lab.Layout{
			Nodes: map[string]lab.Position{
				"vm1":     {X: 100, Y: 100},
				"sw1":     {X: 300, Y: 100},
				"uplink1": {X: 500, Y: 100},
			},
			Links: []lab.LayoutLink{{
				From: lab.LayoutEndpoint{Type: "vm", ID: "vm1"},
				To:   lab.LayoutEndpoint{Type: "switch", ID: "sw1"},
			}, {
				From: lab.LayoutEndpoint{Type: "switch", ID: "sw1"},
				To:   lab.LayoutEndpoint{Type: "external", ID: "uplink1"},
			}},
		},
	}
	rec := requestJSON(t, topology, http.MethodPut, "/api/labs/demo", next)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT lab returned %d: %s", rec.Code, rec.Body.String())
	}
	loaded, err := lab.LoadFile(filepath.Join(workspace, "demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.VMs) != 1 || loaded.VMs[0].ISO != "/home/user/Downloads/alpine.iso" || len(loaded.Switches) != 1 || len(loaded.ExternalLinks) != 1 || len(loaded.Disks) != 1 {
		t.Fatalf("lab document was not persisted declaratively: %+v", loaded)
	}
	if len(loaded.Layout.Nodes) != 3 || len(loaded.Layout.Links) != 2 {
		t.Fatalf("lab layout was not persisted declaratively: %+v", loaded.Layout)
	}
	assertBodyContains(t, topology, http.MethodGet, "/api/labs/demo", `"iso":"/home/user/Downloads/alpine.iso"`)
	assertBodyContains(t, topology, http.MethodGet, "/api/labs/demo", `"externalLinks"`)
}

func TestTopologyConfigSubresourceEndpointsAreNotExposed(t *testing.T) {
	chdirRepoRoot(t)
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "demo.yaml"), `id: demo
name: Demo
vms:
  - id: vm1
    memoryMB: 1024
    cpus: 1
    disk: labs/demo/disks/vm1.qcow2
disks:
  - id: vm1
    path: labs/demo/disks/vm1.qcow2
    sizeGB: 30
    format: qcow2
`)
	topology := NewTopology(Config{Workspace: workspace})

	assertStatus(t, topology, http.MethodGet, "/api/labs/demo/vms", http.StatusNotFound)
	assertStatus(t, topology, http.MethodGet, "/api/labs/demo/vms/vm1", http.StatusNotFound)
	assertStatus(t, topology, http.MethodPost, "/api/labs/demo/vms", http.StatusNotFound)
	assertStatus(t, topology, http.MethodDelete, "/api/labs/demo/vms/vm1", http.StatusNotFound)
	assertStatus(t, topology, http.MethodDelete, "/api/labs/demo/switches/sw1", http.StatusNotFound)
	assertStatus(t, topology, http.MethodPost, "/api/labs/demo/disks", http.StatusNotFound)
	assertStatus(t, topology, http.MethodDelete, "/api/labs/demo/disks/vm1", http.StatusNotFound)
	loaded, err := lab.LoadFile(filepath.Join(workspace, "demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.VMs) != 1 || len(loaded.Disks) != 1 {
		t.Fatalf("subresource endpoint changed lab config: %+v", loaded)
	}
}

func TestCloseManagedWindowPublishesWMCloseEvent(t *testing.T) {
	manager := wm.NewManager()
	addr, err := manager.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := manager.Subscribe(ctx)

	closeManagedWindow(addr, &foxapp.Manifest{
		ID:     "topology",
		Window: foxapp.WindowSpec{Path: "/"},
	}, "http://127.0.0.1:49001")

	select {
	case event := <-events:
		if event.Type != "close-window" || event.AppID != "topology" || event.Host != "127.0.0.1" || event.Port != "49001" || event.Path != "/" {
			t.Fatalf("unexpected close event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close-window event")
	}
}

func TestTopologyKeepsConsoleProxyOutOfTopologyApp(t *testing.T) {
	chdirRepoRoot(t)
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "demo.yaml"), `id: demo
name: Demo
vms:
  - id: vm1
    memoryMB: 1024
    cpus: 1
    disk: labs/demo/disks/vm1.qcow2
    vnc: true
`)
	topology := NewTopology(Config{Workspace: workspace})

	assertStatus(t, topology, http.MethodGet, "/api/labs/demo/vms/vm1/console", http.StatusNotFound)
	assertStatus(t, topology, http.MethodGet, "/api/labs/demo/vms/vm1/console/ws", http.StatusNotFound)
}

func TestTopologyLaunchesVNCViewerPackageWithConsoleTarget(t *testing.T) {
	chdirRepoRoot(t)
	appDir := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	packageRecordingVNCViewer(t, appDir)
	t.Setenv("ARGS_FILE", argsPath)

	topology := &Server{cfg: Config{Workspace: t.TempDir(), WMAddr: "127.0.0.1:1", AppDirs: []string{appDir}}}
	status, err := topology.launchVNCViewer(
		&lab.Lab{ID: "demo"},
		"vm1",
		&virt.ConsoleInfo{Enabled: true, Host: "10.0.0.9", Port: 5903},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(topology.stopConsoleApps)
	if status.State != "running" || status.ID != "vnc-viewer" {
		t.Fatalf("unexpected VNC viewer status: %+v", status)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := string(data)
	for _, want := range []string{
		"--wm-app-id=vnc-viewer",
		"--wm-title=VNC demo / vm1",
		"--vnc-host=10.0.0.9",
		"--vnc-port=5903",
		"--vm-label=demo / vm1",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("recorded viewer args missing %q:\n%s", want, args)
		}
	}
}

func TestTopologyLaunchesTerminalPackageWithShellConsoleTarget(t *testing.T) {
	chdirRepoRoot(t)
	appDir := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	packageRecordingTerminal(t, appDir)
	t.Setenv("ARGS_FILE", argsPath)

	topology := &Server{cfg: Config{
		Workspace:  t.TempDir(),
		LibvirtURI: "qemu:///system",
		WMAddr:     "127.0.0.1:1",
		AppDirs:    []string{appDir},
	}}
	status, err := topology.launchShellConsole(
		&lab.Lab{ID: "demo"},
		lab.VM{ID: "vm1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(topology.stopConsoleApps)
	if status.State != "running" || status.ID != "terminal" {
		t.Fatalf("unexpected terminal status: %+v", status)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := string(data)
	for _, want := range []string{
		"--wm-app-id=terminal",
		"--wm-title=Shell console demo / vm1",
		"--command=exec 'virsh' '-c' 'qemu:///system' 'console' 'foxlab-demo-vm1'",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("recorded terminal args missing %q:\n%s", want, args)
		}
	}
}

func TestTopologyISOsListsDownloadsAndPrefersAlpine(t *testing.T) {
	chdirRepoRoot(t)
	workspace := t.TempDir()
	isoDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("FOXLAB_ISO_DIRS", isoDir)
	t.Setenv("HOME", home)
	writeTestFile(t, filepath.Join(isoDir, "ubuntu.iso"), "iso")
	writeTestFile(t, filepath.Join(isoDir, "alpine-standard.iso"), "iso")

	topology := NewTopology(Config{Workspace: workspace})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/isos", nil)
	topology.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/isos returned %d: %s", rec.Code, rec.Body.String())
	}
	var items []isoItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "alpine-standard.iso" {
		t.Fatalf("unexpected ISO ordering: %+v", items)
	}
}

func TestTopologyListsNetworkInterfaces(t *testing.T) {
	chdirRepoRoot(t)
	topology := NewTopology(Config{Workspace: t.TempDir()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/network-interfaces", nil)
	topology.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/network-interfaces returned %d: %s", rec.Code, rec.Body.String())
	}
	var items []networkInterfaceItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Name == "" || item.Kind == "" {
			t.Fatalf("expected network interface names and kinds, got %+v", items)
		}
	}
}

func packageRecordingTerminal(t *testing.T, outDir string) {
	t.Helper()
	srcDir := t.TempDir()
	binPath := filepath.Join(srcDir, "bin", "terminal")
	writeTestFile(t, filepath.Join(srcDir, foxapp.ManifestFile), `{
  "format": "foxapp.v1",
  "id": "terminal",
  "name": "Terminal",
  "version": "0.1.0",
  "run": {"command": "bin/terminal"},
  "icon": {"type": "builtin", "value": "terminal"},
  "window": {"title": "Terminal", "path": "/"},
  "health": {"path": "/healthz"},
  "web": {"dist": "web/dist"}
}`)
	writeTestFile(t, filepath.Join(srcDir, "web", "dist", "index.html"), "<!doctype html>")
	sourcePath := filepath.Join(t.TempDir(), "main.go")
	writeTestFile(t, sourcePath, recordingAppSource())
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", binPath, sourcePath)
	cmd.Env = append(os.Environ(), "GOCACHE=/tmp/foxlab-go-cache", "GOPROXY=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build recording terminal: %v\n%s", err, output)
	}
	if err := foxapp.Package(srcDir, filepath.Join(outDir, "terminal.foxapp")); err != nil {
		t.Fatal(err)
	}
}

func packageRecordingVNCViewer(t *testing.T, outDir string) {
	t.Helper()
	srcDir := t.TempDir()
	binPath := filepath.Join(srcDir, "bin", "vnc-viewer")
	writeTestFile(t, filepath.Join(srcDir, foxapp.ManifestFile), `{
  "format": "foxapp.v1",
  "id": "vnc-viewer",
  "name": "VNC Viewer",
  "version": "0.1.0",
  "run": {"command": "bin/vnc-viewer"},
  "icon": {"type": "builtin", "value": "monitor"},
  "window": {"title": "VNC Viewer", "path": "/"},
  "health": {"path": "/healthz"},
  "web": {"dist": "web/dist"}
}`)
	writeTestFile(t, filepath.Join(srcDir, "web", "dist", "index.html"), "<!doctype html>")
	sourcePath := filepath.Join(t.TempDir(), "main.go")
	writeTestFile(t, sourcePath, recordingAppSource())
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", binPath, sourcePath)
	cmd.Env = append(os.Environ(), "GOCACHE=/tmp/foxlab-go-cache", "GOPROXY=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build recording VNC viewer: %v\n%s", err, output)
	}
	if err := foxapp.Package(srcDir, filepath.Join(outDir, "vnc-viewer.foxapp")); err != nil {
		t.Fatal(err)
	}
}

func recordingAppSource() string {
	return `package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

func main() {
	addr := "127.0.0.1:0"
	for i, arg := range os.Args[1:] {
		if arg == "--addr" && i+2 < len(os.Args) {
			addr = os.Args[i+2]
		}
	}
	var lines []string
	for i := 1; i < len(os.Args); i++ {
		if i+1 < len(os.Args) && strings.HasPrefix(os.Args[i], "--") {
			lines = append(lines, os.Args[i]+"="+os.Args[i+1])
			i++
			continue
		}
		lines = append(lines, os.Args[i])
	}
	if err := os.WriteFile(os.Getenv("ARGS_FILE"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		panic(err)
	}
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, ` + "`" + `{"status":"ok"}` + "`" + `)
	})
	if err := http.ListenAndServe(addr, nil); err != nil {
		panic(err)
	}
}
`
}

func assertStatus(t *testing.T, srv *http.Server, method, path string, want int) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s returned %d, want %d; body: %s", method, path, rec.Code, want, rec.Body.String())
	}
}

func requestJSON(t *testing.T, srv *http.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func packageTestApp(t *testing.T, outDir string) {
	t.Helper()
	srcDir := t.TempDir()
	writeTestFile(t, filepath.Join(srcDir, foxapp.ManifestFile), `{
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
	writeTestFile(t, filepath.Join(srcDir, "bin", "topology"), "binary")
	writeTestFile(t, filepath.Join(srcDir, "web", "dist", "index.html"), "<!doctype html>")
	if err := foxapp.Package(srcDir, filepath.Join(outDir, "topology.foxapp")); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertBodyContains(t *testing.T, srv *http.Server, method, path, want string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	srv.Handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("%s %s body missing %q:\n%s", method, path, want, rec.Body.String())
	}
}

func chdirRepoRoot(t *testing.T) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	})
}
