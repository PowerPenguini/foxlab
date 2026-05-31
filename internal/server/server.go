package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"foxlab/internal/disk"
	"foxlab/internal/foxapp"
	"foxlab/internal/lab"
	"foxlab/internal/virt"
	"foxlab/internal/wm"
)

type Config struct {
	Addr       string
	Workspace  string
	LibvirtURI string
	StaticDir  string
	AppDirs    []string
	WMAddr     string
}

type Server struct {
	cfg         Config
	mux         *http.ServeMux
	apps        *AppManager
	wm          *wm.Manager
	consoleMu   sync.Mutex
	consoleApps []*launchedConsoleApp
}

type launchedConsoleApp struct {
	cmd      *exec.Cmd
	proc     *appProcess
	cleanup  func()
	manifest *foxapp.Manifest
	url      string
	wmAddr   string
}

func New(cfg Config) *http.Server {
	return NewShell(cfg)
}

func NewShell(cfg Config) *http.Server {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8088"
	}
	if cfg.Workspace == "" {
		cfg.Workspace = "."
	}
	windowManager := wm.NewManager()
	wmAddr, err := windowManager.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wm grpc server failed to start: %v\n", err)
	}
	cfg.WMAddr = wmAddr
	s := &Server{
		cfg:  cfg,
		mux:  http.NewServeMux(),
		wm:   windowManager,
		apps: NewAppManager(cfg, wmAddr),
	}
	s.shellRoutes()
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	srv.RegisterOnShutdown(func() {
		s.apps.StopAll()
		s.stopConsoleApps()
		s.wm.Stop()
	})
	return srv
}

func NewTopology(cfg Config) *http.Server {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8090"
	}
	if cfg.Workspace == "" {
		cfg.Workspace = "."
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.topologyRoutes()
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	srv.RegisterOnShutdown(s.stopConsoleApps)
	return srv
}

func (s *Server) shellRoutes() {
	s.mux.HandleFunc("/api/apps", s.handleApps)
	s.mux.HandleFunc("/api/apps/", s.handleApp)
	s.mux.HandleFunc("/api/wm/events", s.handleWMEvents)
	s.mux.HandleFunc("/", s.handleShellStatic)
}

func (s *Server) topologyRoutes() {
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/api/labs", s.handleLabs)
	s.mux.HandleFunc("/api/labs/", s.handleLab)
	s.mux.HandleFunc("/api/isos", s.handleISOs)
	s.mux.HandleFunc("/api/network-interfaces", s.handleNetworkInterfaces)
	s.mux.HandleFunc("/", s.handleTopologyStatic)
}

func (s *Server) handleWMEvents(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/wm/events" || s.wm == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, fmt.Errorf("event stream is not supported"), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_, _ = fmt.Fprint(w, ": wm connected\n\n")
	flusher.Flush()

	events := s.wm.Subscribe(r.Context())
	for event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
		flusher.Flush()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/apps" || s.apps == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.apps.List())
}

func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	if s.apps == nil {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/apps/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		status, err := s.apps.Status(id)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeJSON(w, status)
	case http.MethodPost:
		status, err := s.apps.Start(id)
		if err != nil {
			if status.State == "missing" {
				writeError(w, err, http.StatusNotFound)
				return
			}
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, status)
	case http.MethodDelete:
		status, err := s.apps.Stop(id)
		if err != nil {
			if status.State == "missing" {
				writeError(w, err, http.StatusNotFound)
				return
			}
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, status)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLabs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/labs" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		files, err := lab.ListFiles(s.cfg.Workspace)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		type item struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Path string `json:"path"`
		}
		var items []item
		for _, path := range files {
			loaded, err := lab.LoadFile(path)
			if err != nil {
				continue
			}
			items = append(items, item{ID: loaded.ID, Name: loaded.Name, Path: path})
		}
		writeJSON(w, items)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLab(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/labs/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	labID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = strings.Join(parts[1:], "/")
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		s.loadLabResponse(w, labID)
	case action == "" && r.Method == http.MethodPut:
		s.saveLabResponse(w, r, labID)
	case action == "apply" && r.Method == http.MethodPost:
		s.applyLabResponse(w, labID)
	case action == "destroy" && r.Method == http.MethodPost:
		s.destroyLabResponse(w, labID)
	case action == "status" && r.Method == http.MethodGet:
		s.statusLabResponse(w, labID)
	case strings.HasPrefix(action, "vms/") && strings.HasSuffix(action, "/console/open") && r.Method == http.MethodPost:
		vmID := strings.TrimSuffix(strings.TrimPrefix(action, "vms/"), "/console/open")
		s.openConsoleResponse(w, labID, vmID)
	case strings.HasPrefix(action, "vms/") && strings.HasSuffix(action, "/text-console/open") && r.Method == http.MethodPost:
		vmID := strings.TrimSuffix(strings.TrimPrefix(action, "vms/"), "/text-console/open")
		s.openTextConsoleResponse(w, labID, vmID)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleISOs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/isos" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	items, err := discoverISOs()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, items)
}

func (s *Server) handleNetworkInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/network-interfaces" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	items, err := discoverNetworkInterfaces()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, items)
}

func (s *Server) loadLabResponse(w http.ResponseWriter, labID string) {
	loaded, err := s.findLab(labID)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	writeJSON(w, loaded)
}

func (s *Server) saveLabResponse(w http.ResponseWriter, r *http.Request, labID string) {
	defer r.Body.Close()
	var incoming lab.Lab
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if incoming.ID == "" {
		incoming.ID = labID
	}
	if incoming.ID != labID {
		writeError(w, fmt.Errorf("lab id in URL and body differ"), http.StatusBadRequest)
		return
	}
	path, err := s.findLabPath(labID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			writeError(w, err, statusFor(err))
			return
		}
		path = filepath.Join(s.cfg.Workspace, labID+".yaml")
	}
	if err := lab.SaveFile(path, &incoming); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	loaded, err := lab.LoadFile(path)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, loaded)
}

func (s *Server) applyLabResponse(w http.ResponseWriter, labID string) {
	loaded, err := s.findLab(labID)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := disk.NewManager().EnsureDeclaredDisks(ctx, loaded); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	driver, err := virt.NewLibvirtDriver(s.cfg.LibvirtURI)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	defer driver.Close()
	if err := driver.Apply(ctx, loaded); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "applied"})
}

func (s *Server) destroyLabResponse(w http.ResponseWriter, labID string) {
	loaded, err := s.findLab(labID)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	driver, err := virt.NewLibvirtDriver(s.cfg.LibvirtURI)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	defer driver.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := driver.Destroy(ctx, loaded); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "destroyed"})
}

func (s *Server) statusLabResponse(w http.ResponseWriter, labID string) {
	loaded, err := s.findLab(labID)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	driver, err := virt.NewLibvirtDriver(s.cfg.LibvirtURI)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	defer driver.Close()
	status, err := driver.Status(context.Background(), loaded)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, status)
}

func (s *Server) openTextConsoleResponse(w http.ResponseWriter, labID, vmID string) {
	if s.cfg.WMAddr == "" {
		writeError(w, fmt.Errorf("wm grpc server is not available"), http.StatusInternalServerError)
		return
	}
	loaded, err := s.findLab(labID)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	vm, ok := labVMByID(loaded, vmID)
	if !ok {
		writeError(w, fmt.Errorf("vm %q not found", vmID), http.StatusNotFound)
		return
	}
	status, err := s.launchTextConsole(loaded, vm)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, status)
}

func (s *Server) openConsoleResponse(w http.ResponseWriter, labID, vmID string) {
	if s.cfg.WMAddr == "" {
		writeError(w, fmt.Errorf("wm grpc server is not available"), http.StatusInternalServerError)
		return
	}
	loaded, err := s.findLab(labID)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	driver, err := virt.NewLibvirtDriver(s.cfg.LibvirtURI)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	defer driver.Close()
	info, err := driver.Console(context.Background(), loaded, vmID)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if !info.Enabled || info.Port <= 0 {
		writeError(w, fmt.Errorf("console is not available"), http.StatusNotFound)
		return
	}
	status, err := s.launchVNCViewer(loaded, vmID, info)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, status)
}

func (s *Server) launchVNCViewer(loaded *lab.Lab, vmID string, info *virt.ConsoleInfo) (AppStatus, error) {
	def, err := s.discoverAppDefinition("vnc-viewer")
	if err != nil {
		return AppStatus{}, err
	}
	extractDir, err := os.MkdirTemp("", "foxapp-vnc-*")
	if err != nil {
		return AppStatus{}, err
	}
	cleanup := func() { _ = os.RemoveAll(extractDir) }
	if err := foxapp.Extract(def.PackagePath, extractDir); err != nil {
		cleanup()
		return AppStatus{}, err
	}
	manifest, err := foxapp.LoadManifestDir(extractDir)
	if err != nil {
		cleanup()
		return AppStatus{}, err
	}
	addr, err := freeLoopbackAddr()
	if err != nil {
		cleanup()
		return AppStatus{}, err
	}
	label := fmt.Sprintf("%s / %s", loaded.ID, vmID)
	cmd := foxapp.PackageCommand(extractDir, manifest, foxapp.Runtime{
		Addr:       addr,
		Workspace:  s.cfg.Workspace,
		LibvirtURI: s.cfg.LibvirtURI,
		WMAddr:     s.cfg.WMAddr,
		WMTitle:    "VNC " + label,
		ExtraArgs: []string{
			"--vnc-host", info.Host,
			"--vnc-port", fmt.Sprint(info.Port),
			"--vm-label", label,
		},
	})
	if err := cmd.Start(); err != nil {
		cleanup()
		return AppStatus{}, err
	}
	proc := &appProcess{done: make(chan struct{})}
	go func() {
		proc.err = cmd.Wait()
		close(proc.done)
	}()
	url := foxapp.URLForAddr(addr)
	s.trackConsoleApp(cmd, cleanup, proc, manifest, url)
	if err := waitForAppReady(url+manifest.Health.Path, proc); err != nil {
		_ = cmd.Process.Kill()
		<-proc.done
		return AppStatus{}, err
	}
	status := statusForManifest(manifest)
	status.State = "running"
	status.URL = url
	return status, nil
}

func (s *Server) launchTextConsole(loaded *lab.Lab, vm lab.VM) (AppStatus, error) {
	def, err := s.discoverAppDefinition("terminal")
	if err != nil {
		return AppStatus{}, err
	}
	extractDir, err := os.MkdirTemp("", "foxapp-terminal-*")
	if err != nil {
		return AppStatus{}, err
	}
	cleanup := func() { _ = os.RemoveAll(extractDir) }
	if err := foxapp.Extract(def.PackagePath, extractDir); err != nil {
		cleanup()
		return AppStatus{}, err
	}
	manifest, err := foxapp.LoadManifestDir(extractDir)
	if err != nil {
		cleanup()
		return AppStatus{}, err
	}
	addr, err := freeLoopbackAddr()
	if err != nil {
		cleanup()
		return AppStatus{}, err
	}
	label := fmt.Sprintf("%s / %s", loaded.ID, vm.ID)
	cmd := foxapp.PackageCommand(extractDir, manifest, foxapp.Runtime{
		Addr:       addr,
		Workspace:  s.cfg.Workspace,
		LibvirtURI: s.cfg.LibvirtURI,
		WMAddr:     s.cfg.WMAddr,
		WMTitle:    "Text console " + label,
		ExtraArgs: []string{
			"--command", "exec " + shellJoin(virshConsoleCommand(s.cfg.LibvirtURI, loaded.ManagedDomainName(vm))),
		},
	})
	if err := cmd.Start(); err != nil {
		cleanup()
		return AppStatus{}, err
	}
	proc := &appProcess{done: make(chan struct{})}
	go func() {
		proc.err = cmd.Wait()
		close(proc.done)
	}()
	url := foxapp.URLForAddr(addr)
	s.trackConsoleApp(cmd, cleanup, proc, manifest, url)
	if err := waitForAppReady(url+manifest.Health.Path, proc); err != nil {
		_ = cmd.Process.Kill()
		<-proc.done
		return AppStatus{}, err
	}
	status := statusForManifest(manifest)
	status.State = "running"
	status.URL = url
	return status, nil
}

func virshConsoleCommand(uri, domain string) []string {
	args := []string{"virsh"}
	if uri != "" {
		args = append(args, "-c", uri)
	}
	return append(args, "console", domain)
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
}

func labVMByID(loaded *lab.Lab, vmID string) (lab.VM, bool) {
	for _, vm := range loaded.VMs {
		if vm.ID == vmID {
			return vm, true
		}
	}
	return lab.VM{}, false
}

func (s *Server) discoverAppDefinition(id string) (AppDefinition, error) {
	refs, err := foxapp.DiscoverPackages(configuredAppDirs(s.cfg))
	if err != nil {
		return AppDefinition{}, err
	}
	for _, ref := range refs {
		if ref.Manifest != nil && ref.Manifest.ID == id {
			return AppDefinition{ID: id, PackagePath: ref.Path, Manifest: ref.Manifest}, nil
		}
	}
	return AppDefinition{}, fmt.Errorf("unknown app %q", id)
}

func (s *Server) trackConsoleApp(cmd *exec.Cmd, cleanup func(), proc *appProcess, manifest *foxapp.Manifest, url string) {
	app := &launchedConsoleApp{cmd: cmd, proc: proc, cleanup: cleanup, manifest: manifest, url: url, wmAddr: s.cfg.WMAddr}
	s.consoleMu.Lock()
	s.consoleApps = append(s.consoleApps, app)
	s.consoleMu.Unlock()
	go func() {
		<-proc.done
		removed := false
		s.consoleMu.Lock()
		for i, current := range s.consoleApps {
			if current == app {
				s.consoleApps = append(s.consoleApps[:i], s.consoleApps[i+1:]...)
				removed = true
				break
			}
		}
		s.consoleMu.Unlock()
		if removed {
			cleanup()
			closeManagedWindow(app.wmAddr, app.manifest, app.url)
		}
	}()
}

func (s *Server) stopConsoleApps() {
	s.consoleMu.Lock()
	apps := append([]*launchedConsoleApp(nil), s.consoleApps...)
	s.consoleApps = nil
	s.consoleMu.Unlock()
	for _, app := range apps {
		if app.cmd != nil && app.cmd.Process != nil {
			_ = app.cmd.Process.Signal(os.Interrupt)
			select {
			case <-app.proc.done:
			case <-time.After(2 * time.Second):
				_ = app.cmd.Process.Kill()
				<-app.proc.done
			}
		}
		if app.cleanup != nil {
			app.cleanup()
		}
		closeManagedWindow(app.wmAddr, app.manifest, app.url)
	}
}

func (s *Server) findLab(id string) (*lab.Lab, error) {
	path, err := s.findLabPath(id)
	if err != nil {
		return nil, err
	}
	return lab.LoadFile(path)
}

func (s *Server) findLabPath(id string) (string, error) {
	files, err := lab.ListFiles(s.cfg.Workspace)
	if err != nil {
		return "", err
	}
	for _, path := range files {
		loaded, err := lab.LoadFile(path)
		if err == nil && loaded.ID == id {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}

type isoItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type networkInterfaceItem struct {
	Name  string `json:"name"`
	Flags string `json:"flags"`
	Kind  string `json:"kind"`
}

func discoverISOs() ([]isoItem, error) {
	dirs := isoSearchDirs()
	seen := map[string]bool{}
	var items []isoItem
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) || os.IsPermission(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".iso") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			abs, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			if seen[abs] {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			seen[abs] = true
			items = append(items, isoItem{Name: entry.Name(), Path: abs, Size: info.Size()})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		ai := strings.Contains(strings.ToLower(items[i].Name), "alpine")
		aj := strings.Contains(strings.ToLower(items[j].Name), "alpine")
		if ai != aj {
			return ai
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func discoverNetworkInterfaces() ([]networkInterfaceItem, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	items := make([]networkInterfaceItem, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Name == "" {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 || strings.HasPrefix(iface.Name, "veth") {
			continue
		}
		kind := networkInterfaceKind(iface.Name)
		items = append(items, networkInterfaceItem{
			Name:  iface.Name,
			Flags: iface.Flags.String(),
			Kind:  kind,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return interfaceSortKey(items[i]) < interfaceSortKey(items[j])
	})
	return items, nil
}

func networkInterfaceKind(name string) string {
	switch {
	case looksLikeLinuxBridge(name) || pathExists(filepath.Join("/sys/class/net", name, "bridge")):
		return "bridge"
	case pathExists(filepath.Join("/sys/class/net", name, "wireless")):
		return "wireless"
	case pathExists(filepath.Join("/sys/class/net", name, "device")):
		return "ethernet"
	default:
		return "interface"
	}
}

func looksLikeLinuxBridge(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "br") || strings.HasPrefix(lower, "virbr") || lower == "docker0"
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func interfaceSortKey(item networkInterfaceItem) string {
	flags := strings.ToLower(item.Flags)
	name := strings.ToLower(item.Name)
	switch {
	case item.Kind == "ethernet" && strings.Contains(flags, "up"):
		return "0-" + name
	case item.Kind == "wireless" && strings.Contains(flags, "up"):
		return "1-" + name
	case item.Kind == "bridge" && strings.Contains(flags, "up"):
		return "2-" + name
	case strings.Contains(flags, "up"):
		return "3-" + name
	default:
		return "5-" + name
	}
}

func isoSearchDirs() []string {
	dirs := filepath.SplitList(os.Getenv("FOXLAB_ISO_DIRS"))
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, "Downloads"), filepath.Join(home, "Pobrane"))
	}
	out := make([]string, 0, len(dirs))
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}

func (s *Server) handleShellStatic(w http.ResponseWriter, r *http.Request) {
	serveStaticDir(w, r, filepath.Join("web", "dist"))
}

func (s *Server) handleTopologyStatic(w http.ResponseWriter, r *http.Request) {
	root := s.cfg.StaticDir
	if root == "" {
		root = filepath.Join("apps", "topology", "web", "dist")
	}
	serveStaticDir(w, r, root)
}

func serveStaticDir(w http.ResponseWriter, r *http.Request, root string) {
	if r.URL.Path == "/" {
		indexPath := filepath.Join(root, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			http.ServeFile(w, r, indexPath)
			return
		}
		writeError(w, fmt.Errorf("web app is not built: missing %s", indexPath), http.StatusServiceUnavailable)
		return
	}
	path := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if path == "." || strings.HasPrefix(path, "..") {
		http.NotFound(w, r)
		return
	}
	candidate := filepath.Join(root, path)
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		http.ServeFile(w, r, candidate)
		return
	}
	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func statusFor(err error) int {
	if errors.Is(err, os.ErrNotExist) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
