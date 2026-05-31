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

type extractedApp struct {
	dir      string
	manifest *foxapp.Manifest
	cleanup  func()
}

type appProcess struct {
	done chan struct{}
	err  error
}

type managedWindowTarget struct {
	host string
	port string
	path string
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

func (m *AppManager) Start(id string, opts AppStartOptions) (AppStatus, error) {
	def, err := m.definition(id)
	if err != nil {
		return AppStatus{ID: id, State: "missing", Error: err.Error()}, err
	}
	status := statusForManifest(def.Manifest)

	if status, app, ok := m.runningStatus(id); ok {
		if opts.WMPath != "" {
			_ = openManagedWindow(m.wmAddr, app.manifest, app.url, opts.WMPath)
		}
		return status, nil
	}

	if m.wmAddr == "" {
		return m.errorStatus(id, status, fmt.Errorf("wm grpc server is not available"))
	}

	app, err := extractApp(def.PackagePath)
	if err != nil {
		return m.errorStatus(id, status, err)
	}
	status = statusForManifest(app.manifest)

	running, err := m.launchExtractedApp(id, app, opts)
	if err != nil {
		return m.errorStatus(id, status, err)
	}

	if err := waitForAppReady(running.url+running.manifest.Health.Path, running.proc); err != nil {
		_ = running.cmd.Process.Kill()
		<-running.proc.done
		m.removeRunning(id, running.cmd, err)
		return errorAppStatus(status, err), err
	}

	status.State = "running"
	status.URL = running.url
	return status, nil
}

func extractApp(packagePath string) (*extractedApp, error) {
	dir, err := os.MkdirTemp("", "foxapp-*")
	if err != nil {
		return nil, err
	}
	app := &extractedApp{
		dir:     dir,
		cleanup: func() { _ = os.RemoveAll(dir) },
	}
	if err := foxapp.Extract(packagePath, dir); err != nil {
		app.cleanup()
		return nil, err
	}
	manifest, err := foxapp.LoadManifestDir(dir)
	if err != nil {
		app.cleanup()
		return nil, err
	}
	app.manifest = manifest
	return app, nil
}

func (m *AppManager) launchExtractedApp(id string, app *extractedApp, opts AppStartOptions) (*runningApp, error) {
	addr, err := freeLoopbackAddr()
	if err != nil {
		app.cleanup()
		return nil, err
	}
	cmd := foxapp.PackageCommand(app.dir, app.manifest, foxapp.Runtime{
		Addr:       addr,
		Workspace:  m.cfg.Workspace,
		LibvirtURI: m.cfg.LibvirtURI,
		WMAddr:     m.wmAddr,
		WMPath:     opts.WMPath,
		Env:        []string{"FOXLAB_APP_DIRS=" + strings.Join(m.appDirs, string(os.PathListSeparator))},
	})
	if err := cmd.Start(); err != nil {
		app.cleanup()
		return nil, err
	}

	proc := &appProcess{done: make(chan struct{})}
	go func() {
		proc.err = cmd.Wait()
		close(proc.done)
	}()

	running := &runningApp{
		cmd:      cmd,
		proc:     proc,
		url:      foxapp.URLForAddr(addr),
		manifest: app.manifest,
		cleanup:  app.cleanup,
	}
	m.mu.Lock()
	m.running[id] = running
	m.lastErr[id] = ""
	m.mu.Unlock()

	go m.trackExit(id, cmd, proc)
	return running, nil
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

func (m *AppManager) runningStatus(id string) (AppStatus, *runningApp, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	app := m.running[id]
	if app == nil {
		return AppStatus{}, nil, false
	}
	status := statusForManifest(app.manifest)
	status.State = "running"
	status.URL = app.url
	return status, app, true
}

func (m *AppManager) errorStatus(id string, status AppStatus, err error) (AppStatus, error) {
	m.setError(id, err)
	return errorAppStatus(status, err), err
}

func errorAppStatus(status AppStatus, err error) AppStatus {
	status.State = "error"
	status.Error = err.Error()
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
	target, err := managedWindowTargetFor(manifest, rawURL, "")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = wm.CloseWindow(ctx, wmAddr, wm.CloseWindowRequest{
		AppID: manifest.ID,
		Host:  target.host,
		Port:  target.port,
		Path:  target.path,
	})
}

func openManagedWindow(wmAddr string, manifest *foxapp.Manifest, rawURL, path string) error {
	if wmAddr == "" || manifest == nil || rawURL == "" {
		return fmt.Errorf("window manager is not available")
	}
	target, err := managedWindowTargetFor(manifest, rawURL, path)
	if err != nil {
		return err
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
		Host: target.host,
		Port: target.port,
		Path: target.path,
	})
}

func managedWindowTargetFor(manifest *foxapp.Manifest, rawURL, path string) (managedWindowTarget, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return managedWindowTarget{}, err
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return managedWindowTarget{}, err
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
	return managedWindowTarget{host: host, port: port, path: path}, nil
}
