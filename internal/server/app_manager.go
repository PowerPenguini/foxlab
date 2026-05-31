package server

import (
	"context"
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

	"foxlab/internal/foxapp"
	"foxlab/internal/wm"
)

type AppStatus struct {
	State       string          `json:"state"`
	URL         string          `json:"url,omitempty"`
	Error       string          `json:"error,omitempty"`
	ID          string          `json:"id,omitempty"`
	Name        string          `json:"name,omitempty"`
	WindowTitle string          `json:"windowTitle,omitempty"`
	Icon        foxapp.IconSpec `json:"icon,omitempty"`
}

type AppManager struct {
	cfg     Config
	wmAddr  string
	appDirs []string

	mu      sync.Mutex
	running map[string]*runningApp
	lastErr map[string]string
}

type AppDefinition struct {
	ID          string
	PackagePath string
	Manifest    *foxapp.Manifest
}

type AppStartOptions struct {
	WMPath string
}

type runningApp struct {
	cmd      *exec.Cmd
	proc     *appProcess
	url      string
	manifest *foxapp.Manifest
	cleanup  func()
}

type appProcess struct {
	done chan struct{}
	err  error
}

func NewAppManager(cfg Config, wmAddr string) *AppManager {
	appDirs := configuredAppDirs(cfg)
	return &AppManager{
		cfg:     cfg,
		wmAddr:  wmAddr,
		appDirs: appDirs,
		running: make(map[string]*runningApp),
		lastErr: make(map[string]string),
	}
}

func (m *AppManager) List() []AppStatus {
	defs, _ := m.discover()
	ids := make([]string, 0, len(defs))
	for id := range defs {
		ids = append(ids, id)
	}
	m.mu.Lock()
	for id := range m.running {
		if _, ok := defs[id]; !ok {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	sort.Strings(ids)

	items := make([]AppStatus, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		def := defs[id]
		status := m.statusFromDefinition(id, def)
		items = append(items, status)
	}
	return items
}

func configuredAppDirs(cfg Config) []string {
	appDirs := cfg.AppDirs
	if len(appDirs) == 0 {
		appDirs = foxapp.DefaultAppDirs()
	}
	return absoluteAppDirs(appDirs)
}

func (m *AppManager) Status(id string) (AppStatus, error) {
	def, err := m.definition(id)
	if err != nil {
		return AppStatus{ID: id, State: "missing", Error: err.Error()}, err
	}
	return m.statusFromDefinition(id, def), nil
}

func (m *AppManager) Start(id string, options ...AppStartOptions) (AppStatus, error) {
	opts := appStartOptions(options)
	def, err := m.definition(id)
	if err != nil {
		return AppStatus{ID: id, State: "missing", Error: err.Error()}, err
	}
	status := statusForManifest(def.Manifest)

	m.mu.Lock()
	if app := m.running[id]; app != nil {
		status = statusForManifest(app.manifest)
		status.State = "running"
		status.URL = app.url
		m.mu.Unlock()
		if opts.WMPath != "" {
			_ = openManagedWindow(m.wmAddr, app.manifest, app.url, opts.WMPath)
		}
		return status, nil
	}
	m.mu.Unlock()

	if m.wmAddr == "" {
		err := fmt.Errorf("wm grpc server is not available")
		m.setError(id, err)
		status.State = "error"
		status.Error = err.Error()
		return status, err
	}
	extractDir, err := os.MkdirTemp("", "foxapp-*")
	if err != nil {
		m.setError(id, err)
		status.State = "error"
		status.Error = err.Error()
		return status, err
	}
	cleanup := func() { _ = os.RemoveAll(extractDir) }
	if err := foxapp.Extract(def.PackagePath, extractDir); err != nil {
		cleanup()
		m.setError(id, err)
		status.State = "error"
		status.Error = err.Error()
		return status, err
	}
	manifest, err := foxapp.LoadManifestDir(extractDir)
	if err != nil {
		cleanup()
		m.setError(id, err)
		status.State = "error"
		status.Error = err.Error()
		return status, err
	}
	status = statusForManifest(manifest)
	addr, err := freeLoopbackAddr()
	if err != nil {
		cleanup()
		status.State = "error"
		status.Error = err.Error()
		return status, err
	}

	cmd := foxapp.PackageCommand(extractDir, manifest, foxapp.Runtime{
		Addr:       addr,
		Workspace:  m.cfg.Workspace,
		LibvirtURI: m.cfg.LibvirtURI,
		WMAddr:     m.wmAddr,
		WMPath:     opts.WMPath,
		Env:        []string{"FOXLAB_APP_DIRS=" + strings.Join(m.appDirs, string(os.PathListSeparator))},
	})
	if err := cmd.Start(); err != nil {
		cleanup()
		m.setError(id, err)
		status.State = "error"
		status.Error = err.Error()
		return status, err
	}

	proc := &appProcess{done: make(chan struct{})}
	go func() {
		proc.err = cmd.Wait()
		close(proc.done)
	}()

	url := foxapp.URLForAddr(addr)
	m.mu.Lock()
	m.running[id] = &runningApp{cmd: cmd, proc: proc, url: url, manifest: manifest, cleanup: cleanup}
	m.lastErr[id] = ""
	m.mu.Unlock()

	go m.trackExit(id, cmd, proc)

	if err := waitForAppReady(url+manifest.Health.Path, proc); err != nil {
		_ = cmd.Process.Kill()
		<-proc.done
		m.removeRunning(id, cmd, err)
		status.State = "error"
		status.Error = err.Error()
		return status, err
	}

	status.State = "running"
	status.URL = url
	return status, nil
}

func appStartOptions(options []AppStartOptions) AppStartOptions {
	if len(options) == 0 {
		return AppStartOptions{}
	}
	return options[0]
}

func (m *AppManager) Stop(id string) (AppStatus, error) {
	status, err := m.Status(id)
	if err != nil {
		return status, err
	}
	m.mu.Lock()
	app := m.running[id]
	delete(m.running, id)
	m.lastErr[id] = ""
	m.mu.Unlock()

	if app == nil || app.cmd == nil || app.cmd.Process == nil {
		status.State = "stopped"
		return status, nil
	}

	_ = app.cmd.Process.Signal(os.Interrupt)
	select {
	case <-app.proc.done:
	case <-time.After(2 * time.Second):
		_ = app.cmd.Process.Kill()
		<-app.proc.done
	}
	if app.cleanup != nil {
		app.cleanup()
	}
	closeManagedWindow(m.wmAddr, app.manifest, app.url)
	status.State = "stopped"
	return status, nil
}

func (m *AppManager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_, _ = m.Stop(id)
	}
}

func (m *AppManager) trackExit(id string, cmd *exec.Cmd, proc *appProcess) {
	<-proc.done
	m.removeRunning(id, cmd, proc.err)
}

func (m *AppManager) removeRunning(id string, cmd *exec.Cmd, err error) {
	m.mu.Lock()
	app := m.running[id]
	if app == nil || app.cmd != cmd {
		m.mu.Unlock()
		return
	}
	delete(m.running, id)
	if err != nil {
		m.lastErr[id] = err.Error()
	} else {
		m.lastErr[id] = ""
	}
	cleanup := app.cleanup
	manifest := app.manifest
	url := app.url
	m.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
	closeManagedWindow(m.wmAddr, manifest, url)
}

func (m *AppManager) setError(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.running, id)
	m.lastErr[id] = err.Error()
}

func (m *AppManager) definition(id string) (AppDefinition, error) {
	defs, err := m.discover()
	if err != nil {
		return AppDefinition{}, err
	}
	if def, ok := defs[id]; ok {
		return def, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if app := m.running[id]; app != nil {
		return AppDefinition{ID: id, Manifest: app.manifest}, nil
	}
	return AppDefinition{}, fmt.Errorf("unknown app %q", id)
}

func (m *AppManager) discover() (map[string]AppDefinition, error) {
	refs, err := foxapp.DiscoverPackages(m.appDirs)
	if err != nil {
		return nil, err
	}
	defs := map[string]AppDefinition{}
	for _, ref := range refs {
		if ref.Manifest == nil {
			continue
		}
		id := ref.Manifest.ID
		if _, exists := defs[id]; exists {
			continue
		}
		defs[id] = AppDefinition{
			ID:          id,
			PackagePath: filepath.Clean(ref.Path),
			Manifest:    ref.Manifest,
		}
	}
	return defs, nil
}

func (m *AppManager) statusFromDefinition(id string, def AppDefinition) AppStatus {
	status := AppStatus{ID: id, State: "stopped"}
	if def.Manifest != nil {
		status = statusForManifest(def.Manifest)
		status.State = "stopped"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if app := m.running[id]; app != nil {
		status = statusForManifest(app.manifest)
		status.State = "running"
		status.URL = app.url
		return status
	}
	if err := m.lastErr[id]; err != "" {
		status.State = "error"
		status.Error = err
		return status
	}
	return status
}

func absoluteAppDirs(appDirs []string) []string {
	out := make([]string, 0, len(appDirs))
	for _, dir := range appDirs {
		if dir == "" {
			continue
		}
		if filepath.IsAbs(dir) {
			out = append(out, filepath.Clean(dir))
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			out = append(out, filepath.Clean(dir))
			continue
		}
		out = append(out, abs)
	}
	return out
}

func statusForManifest(manifest *foxapp.Manifest) AppStatus {
	return AppStatus{
		ID:          manifest.ID,
		Name:        manifest.Name,
		WindowTitle: manifest.Window.Title,
		Icon:        manifest.Icon,
	}
}

func freeLoopbackAddr() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	return listener.Addr().String(), nil
}

func waitForAppReady(url string, proc *appProcess) error {
	client := http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-proc.done:
			if proc.err != nil {
				return fmt.Errorf("app exited before readiness: %w", proc.err)
			}
			return fmt.Errorf("app exited before readiness")
		default:
		}

		res, err := client.Get(url)
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("app did not become ready")
}

func closeManagedWindow(wmAddr string, manifest *foxapp.Manifest, rawURL string) {
	if wmAddr == "" || manifest == nil || rawURL == "" {
		return
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return
	}
	path := manifest.Window.Path
	if path == "" {
		path = "/"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = wm.CloseWindow(ctx, wmAddr, wm.CloseWindowRequest{
		AppID: manifest.ID,
		Host:  host,
		Port:  port,
		Path:  path,
	})
}

func openManagedWindow(wmAddr string, manifest *foxapp.Manifest, rawURL, path string) error {
	if wmAddr == "" || manifest == nil || rawURL == "" {
		return fmt.Errorf("window manager is not available")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return err
	}
	if path == "" {
		path = manifest.Window.Path
	}
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return wm.OpenWindow(ctx, wmAddr, wm.OpenWindowRequest{
		AppID: manifest.ID,
		Name:  manifest.Name,
		Title: manifest.Window.Title,
		Icon: wm.Icon{
			Type:  manifest.Icon.Type,
			Value: manifest.Icon.Value,
		},
		Host: host,
		Port: port,
		Path: path,
	})
}
