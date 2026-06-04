package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
	"google.golang.org/grpc"
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
	s := &Server{
		cfg: cfg,
		mux: http.NewServeMux(),
		wm:  wm.NewManager(),
	}
	wmAddr, err := s.wm.Start(s.registerShellControl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wm grpc server failed to start: %v\n", err)
	}
	cfg.WMAddr = wmAddr
	s.cfg = cfg
	s.apps = NewAppManager(cfg, wmAddr)
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

func (s *Server) registerShellControl(server *grpc.Server) {
	wm.RegisterShellControlServer(server, s)
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
	s.mux.HandleFunc("/api/desktop", s.handleDesktop)
	s.mux.HandleFunc("/api/files/open", s.handleOpenFile)
	s.mux.HandleFunc("/api/wm/events", s.handleWMEvents)
	s.mux.HandleFunc("/api/wm/windows", s.handleWMWindows)
	s.mux.HandleFunc("/", s.handleShellStatic)
}

func (s *Server) topologyRoutes() {
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/api/lab-file", s.handleLabFile)
	s.mux.HandleFunc("/api/lab-file/", s.handleLabFile)
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

func (s *Server) handleWMWindows(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/wm/windows" || s.wm == nil {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		windows := s.wm.Windows()
		sort.Slice(windows, func(i, j int) bool {
			return windows[i].Placement.Z < windows[j].Placement.Z
		})
		writeJSON(w, windows)
	case http.MethodPatch:
		id := r.URL.Query().Get("id")
		if id == "" {
			writeError(w, fmt.Errorf("missing window id"), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		var update wm.WindowUpdate
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		window, err := s.wm.UpdateWindow(id, update)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeJSON(w, window)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			writeError(w, fmt.Errorf("missing window id"), http.StatusBadRequest)
			return
		}
		window, err := s.wm.ForgetWindow(id)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeJSON(w, window)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
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
		status, err := s.apps.Start(id, AppStartOptions{WMPath: r.URL.Query().Get("path")})
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

type desktopListResponse struct {
	Path    string             `json:"path"`
	Parent  string             `json:"parent,omitempty"`
	Entries []desktopEntryItem `json:"entries"`
}

type desktopEntryItem struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

type openFileRequest struct {
	Path string `json:"path"`
}

type openFileResponse struct {
	Path       string                `json:"path"`
	AppID      string                `json:"appID"`
	WindowPath string                `json:"windowPath"`
	Handler    openFileHandlerResult `json:"handler"`
	Status     AppStatus             `json:"status"`
}

type openFileHandlerResult struct {
	AppID      string   `json:"appID"`
	Kind       string   `json:"kind,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	Fallback   bool     `json:"fallback,omitempty"`
	Priority   int      `json:"priority,omitempty"`
}

type openFileMatch struct {
	def     AppDefinition
	handler foxapp.FileHandlerSpec
}

func (s *Server) handleDesktop(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/desktop" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	root, err := filepath.Abs(s.cfg.Workspace)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	path, err := cleanDesktopPath(root, r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	entries, err := desktopEntries(path)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	writeJSON(w, desktopListResponse{
		Path:    path,
		Parent:  desktopParent(root, path),
		Entries: entries,
	})
}

func (s *Server) handleOpenFile(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/files/open" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req openFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	response, err := s.openFile(req.Path)
	if err != nil {
		writeError(w, err, openFileStatusFor(err))
		return
	}
	writeJSON(w, response)
}

func (s *Server) OpenFile(ctx context.Context, req *wm.ShellOpenFileRequest) (*wm.ShellOpenFileResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("missing open file request")
	}
	response, err := s.openFile(req.Path)
	if err != nil {
		return nil, err
	}
	return &wm.ShellOpenFileResponse{
		Path:       response.Path,
		AppID:      response.AppID,
		WindowPath: response.WindowPath,
		URL:        response.Status.URL,
	}, nil
}

func (s *Server) openFile(rawPath string) (openFileResponse, error) {
	if s.apps == nil {
		return openFileResponse{}, fmt.Errorf("app manager is not available")
	}
	path, err := cleanOpenFilePath(rawPath)
	if err != nil {
		return openFileResponse{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return openFileResponse{}, err
	}
	match, err := s.openFileMatch(path, info)
	if err != nil {
		return openFileResponse{}, err
	}
	windowPath := renderOpenFilePath(match.handler.OpenPath, path)
	status, err := s.apps.Start(match.def.ID, AppStartOptions{WMPath: windowPath})
	if err != nil {
		return openFileResponse{}, err
	}
	return openFileResponse{
		Path:       path,
		AppID:      match.def.ID,
		WindowPath: windowPath,
		Handler: openFileHandlerResult{
			AppID:      match.def.ID,
			Kind:       match.handler.Kind,
			Extensions: match.handler.Extensions,
			Fallback:   match.handler.Fallback,
			Priority:   match.handler.Priority,
		},
		Status: status,
	}, nil
}

func openFileStatusFor(err error) int {
	if err == nil {
		return http.StatusOK
	}
	msg := err.Error()
	if strings.Contains(msg, "no app can open") {
		return http.StatusNotFound
	}
	if strings.Contains(msg, "path is required") || strings.Contains(msg, "path must be absolute") {
		return http.StatusBadRequest
	}
	return statusFor(err)
}

func (s *Server) handleLabFile(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/lab-file" && !strings.HasPrefix(r.URL.Path, "/api/lab-file/") {
		http.NotFound(w, r)
		return
	}
	path, err := labFilePathFromRequest(r)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/lab-file"), "/")
	switch {
	case action == "" && r.Method == http.MethodGet:
		s.loadLabFileResponse(w, path)
	case action == "" && r.Method == http.MethodPut:
		s.saveLabFileResponse(w, r, path)
	case action == "apply" && r.Method == http.MethodPost:
		s.applyLabFileResponse(w, path)
	case action == "destroy" && r.Method == http.MethodPost:
		s.destroyLabFileResponse(w, path)
	case action == "status" && r.Method == http.MethodGet:
		s.statusLabFileResponse(w, path)
	case strings.HasPrefix(action, "vms/") && strings.HasSuffix(action, "/console/open") && r.Method == http.MethodPost:
		vmID := strings.TrimSuffix(strings.TrimPrefix(action, "vms/"), "/console/open")
		s.openConsoleFileResponse(w, path, vmID)
	case strings.HasPrefix(action, "vms/") && strings.HasSuffix(action, "/shell-console/open") && r.Method == http.MethodPost:
		vmID := strings.TrimSuffix(strings.TrimPrefix(action, "vms/"), "/shell-console/open")
		s.openShellConsoleFileResponse(w, path, vmID)
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

func (s *Server) loadLabFileResponse(w http.ResponseWriter, path string) {
	loaded, err := lab.LoadFile(path)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	writeJSON(w, loaded)
}

func (s *Server) saveLabFileResponse(w http.ResponseWriter, r *http.Request, path string) {
	defer r.Body.Close()
	var incoming lab.Lab
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
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

func (s *Server) applyLabFileResponse(w http.ResponseWriter, path string) {
	loaded, err := lab.LoadFile(path)
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

func (s *Server) destroyLabFileResponse(w http.ResponseWriter, path string) {
	loaded, err := lab.LoadFile(path)
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

func (s *Server) statusLabFileResponse(w http.ResponseWriter, path string) {
	loaded, err := lab.LoadFile(path)
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

func (s *Server) openShellConsoleFileResponse(w http.ResponseWriter, path, vmID string) {
	if s.cfg.WMAddr == "" {
		writeError(w, fmt.Errorf("wm grpc server is not available"), http.StatusInternalServerError)
		return
	}
	loaded, err := lab.LoadFile(path)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	vm, ok := labVMByID(loaded, vmID)
	if !ok {
		writeError(w, fmt.Errorf("vm %q not found", vmID), http.StatusNotFound)
		return
	}
	status, err := s.launchShellConsole(loaded, vm)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, status)
}

func (s *Server) openConsoleFileResponse(w http.ResponseWriter, path, vmID string) {
	if s.cfg.WMAddr == "" {
		writeError(w, fmt.Errorf("wm grpc server is not available"), http.StatusInternalServerError)
		return
	}
	loaded, err := lab.LoadFile(path)
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

func (s *Server) launchShellConsole(loaded *lab.Lab, vm lab.VM) (AppStatus, error) {
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
		WMTitle:    "Shell console " + label,
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

func cleanDesktopPath(root, raw string) (string, error) {
	if raw == "" {
		return filepath.Clean(root), nil
	}
	var path string
	if filepath.IsAbs(raw) {
		path = filepath.Clean(raw)
	} else {
		path = filepath.Clean(filepath.Join(root, raw))
	}
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", fmt.Errorf("desktop path escapes workspace")
	}
	return path, nil
}

func desktopEntries(path string) ([]desktopEntryItem, error) {
	items, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	entries := make([]desktopEntryItem, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.Name(), ".") {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		itemType := "file"
		if info.IsDir() {
			itemType = "dir"
		}
		entries = append(entries, desktopEntryItem{
			Name:     item.Name(),
			Path:     filepath.Join(path, item.Name()),
			Type:     itemType,
			Size:     info.Size(),
			Modified: info.ModTime().Format(time.RFC3339),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "dir"
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

func desktopParent(root, path string) string {
	clean := filepath.Clean(path)
	if clean == filepath.Clean(root) {
		return ""
	}
	parent := filepath.Dir(clean)
	if parent == clean || parent == "." {
		return ""
	}
	if parent != root && !strings.HasPrefix(parent, root+string(filepath.Separator)) {
		return ""
	}
	return parent
}

func cleanOpenFilePath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("path must be absolute")
	}
	return filepath.Clean(raw), nil
}

func (s *Server) openFileMatch(path string, info os.FileInfo) (openFileMatch, error) {
	if s.apps == nil {
		return openFileMatch{}, fmt.Errorf("app manager is not available")
	}
	defs, err := s.apps.discover()
	if err != nil {
		return openFileMatch{}, err
	}
	var matches []openFileMatch
	for _, def := range defs {
		if def.Manifest == nil {
			continue
		}
		for _, handler := range def.Manifest.Handlers {
			if fileHandlerMatches(handler, path, info) {
				matches = append(matches, openFileMatch{def: def, handler: handler})
			}
		}
	}
	if len(matches) == 0 {
		return openFileMatch{}, fmt.Errorf("no app can open %s", path)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].handler.Priority != matches[j].handler.Priority {
			return matches[i].handler.Priority > matches[j].handler.Priority
		}
		return matches[i].def.ID < matches[j].def.ID
	})
	return matches[0], nil
}

func fileHandlerMatches(handler foxapp.FileHandlerSpec, path string, info os.FileInfo) bool {
	if info.IsDir() {
		return handler.Kind == "directory"
	}
	if handler.Kind != "" && handler.Kind != "file" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, candidate := range handler.Extensions {
		if strings.ToLower(candidate) == ext {
			return true
		}
	}
	return handler.Fallback
}

func renderOpenFilePath(template, path string) string {
	parent := filepath.Dir(path)
	out := strings.ReplaceAll(template, "{path}", url.QueryEscape(path))
	out = strings.ReplaceAll(out, "{parent}", url.QueryEscape(parent))
	return out
}

func labFilePathFromRequest(r *http.Request) (string, error) {
	path, err := cleanOpenFilePath(r.URL.Query().Get("path"))
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(path), lab.FileExtension) {
		return "", fmt.Errorf("path must point to a %s file", lab.FileExtension)
	}
	return path, nil
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
