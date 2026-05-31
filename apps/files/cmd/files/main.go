package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"foxlab/internal/wm"
)

const (
	readLimit     = 1024 * 1024
	idleUnmount   = 10 * time.Minute
	helperTimeout = 45 * time.Second
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "helper" {
		if err := runHelper(os.Stdin, os.Stdout); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fs := flag.NewFlagSet("files", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8093", "HTTP listen address")
	workspace := fs.String("workspace", ".", "workspace directory")
	uri := fs.String("uri", "", "libvirt URI")
	wmAddr := fs.String("wm-addr", "", "window manager gRPC address")
	wmAppID := fs.String("wm-app-id", "files", "window manager app id")
	wmName := fs.String("wm-name", "Files", "window manager app name")
	wmTitle := fs.String("wm-title", "Files", "window manager window title")
	wmIconType := fs.String("wm-icon-type", "builtin", "window manager icon type")
	wmIconValue := fs.String("wm-icon-value", "folder", "window manager icon value")
	wmPath := fs.String("wm-path", "/", "path to open in the window")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: files [--addr 127.0.0.1:8093] [--workspace .] [--wm-addr 127.0.0.1:12345]")
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])

	app := newFilesApp(cleanWorkspace(*workspace), *uri)
	defer app.cleanupAll(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", app.health)
	mux.HandleFunc("/api/fs/list", app.listFS)
	mux.HandleFunc("/api/fs/read", app.readFS)
	mux.HandleFunc("/api/fs/download", app.downloadFS)
	mux.HandleFunc("/api/fs/mkdir", app.mkdirFS)
	mux.HandleFunc("/api/fs/rename", app.renameFS)
	mux.HandleFunc("/api/fs/delete", app.deleteFS)
	mux.HandleFunc("/api/fs/copy", app.copyFS)
	mux.HandleFunc("/api/images/discover", app.discoverImages)
	mux.HandleFunc("/api/images/info", app.imageInfo)
	mux.HandleFunc("/api/images/layers", app.imageLayers)
	mux.HandleFunc("/api/images/mount", app.mountImage)
	mux.HandleFunc("/api/images/unmount", app.unmountImage)
	mux.HandleFunc("/api/layers/diff", app.layerDiff)
	mux.HandleFunc("/", app.staticFile)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("Foxlab files listening at http://%s\n", *addr)
	if *wmAddr != "" {
		openRequest := wm.OpenWindowRequest{
			AppID: *wmAppID,
			Name:  *wmName,
			Title: *wmTitle,
			Icon:  wm.Icon{Type: *wmIconType, Value: *wmIconValue},
			Path:  *wmPath,
		}
		normalizeWindowTarget(*addr, &openRequest.Host, &openRequest.Port, &openRequest.Path)
		go notifyWMWhenReady(*wmAddr, openRequest)
	}

	go app.reapIdleMounts()
	serveUntilInterrupted(srv)
}

type filesApp struct {
	workspace string
	libvirt   string
	static    string
	helper    string

	mu     sync.Mutex
	mounts map[string]*mountSession
}

type mountSession struct {
	ID        string    `json:"id"`
	Image     string    `json:"image"`
	Path      string    `json:"path"`
	NBD       string    `json:"nbd"`
	MountedAt time.Time `json:"mountedAt"`
	LastUsed  time.Time `json:"lastUsed"`
	ReadOnly  bool      `json:"readOnly"`
}

type fileEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	Modified string `json:"modified"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

type fsListResponse struct {
	Path      string          `json:"path"`
	Parent    string          `json:"parent"`
	ReadOnly  bool            `json:"readOnly"`
	Entries   []fileEntry     `json:"entries"`
	Mounts    []*mountSession `json:"mounts"`
	Workspace string          `json:"workspace"`
}

type fsReadResponse struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Data      string `json:"data"`
}

type writePathRequest struct {
	Path string `json:"path"`
}

type renameRequest struct {
	Path string `json:"path"`
	To   string `json:"to"`
}

type copyRequest struct {
	Path string `json:"path"`
	To   string `json:"to"`
}

type mountRequest struct {
	Path string `json:"path"`
}

type unmountRequest struct {
	ID string `json:"id"`
}

type imageSummary struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

type qemuInfo struct {
	Filename            string `json:"filename"`
	Format              string `json:"format"`
	VirtualSize         int64  `json:"virtual-size"`
	ActualSize          int64  `json:"actual-size"`
	DirtyFlag           bool   `json:"dirty-flag"`
	BackingFilename     string `json:"backing-filename"`
	FullBackingFilename string `json:"full-backing-filename"`
}

type layerInfo struct {
	Path        string `json:"path"`
	Format      string `json:"format"`
	VirtualSize int64  `json:"virtualSize"`
	ActualSize  int64  `json:"actualSize"`
	Backing     string `json:"backing,omitempty"`
}

type imageInfoResponse struct {
	Info   qemuInfo    `json:"info"`
	Layers []layerInfo `json:"layers"`
}

type helperRequest struct {
	Action     string `json:"action"`
	Image      string `json:"image,omitempty"`
	MountPoint string `json:"mountPoint,omitempty"`
	NBD        string `json:"nbd,omitempty"`
}

type helperResponse struct {
	MountPoint string `json:"mountPoint,omitempty"`
	NBD        string `json:"nbd,omitempty"`
}

func newFilesApp(workspace, libvirt string) *filesApp {
	helper, _ := os.Executable()
	return &filesApp{
		workspace: workspace,
		libvirt:   libvirt,
		static:    filepath.Join("web", "dist"),
		helper:    helper,
		mounts:    map[string]*mountSession{},
	}
}

func (a *filesApp) health(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (a *filesApp) listFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	clean, err := cleanAnyPath(path)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	entries, err := listDirectory(clean, a.pathReadOnly(clean))
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	writeJSON(w, fsListResponse{
		Path:      clean,
		Parent:    parentPath(clean),
		ReadOnly:  a.pathReadOnly(clean),
		Entries:   entries,
		Mounts:    a.mountList(),
		Workspace: a.workspace,
	})
}

func (a *filesApp) readFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path, err := cleanAnyPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	if info.IsDir() {
		writeError(w, fmt.Errorf("%s is a directory", path), http.StatusBadRequest)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, readLimit+1))
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	truncated := len(data) > readLimit
	if truncated {
		data = data[:readLimit]
	}
	a.touchMount(path)
	writeJSON(w, fsReadResponse{
		Path:      path,
		Name:      filepath.Base(path),
		Size:      info.Size(),
		Truncated: truncated,
		Data:      string(data),
	})
}

func (a *filesApp) downloadFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path, err := cleanAnyPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	if info.IsDir() {
		writeError(w, fmt.Errorf("%s is a directory", path), http.StatusBadRequest)
		return
	}
	a.touchMount(path)
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(path)))
	http.ServeFile(w, r, path)
}

func (a *filesApp) mkdirFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req writePathRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	path, err := a.cleanWritablePath(req.Path)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	writeJSON(w, map[string]string{"status": "created", "path": path})
}

func (a *filesApp) renameFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req renameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	from, err := a.cleanWritablePath(req.Path)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	to, err := a.cleanWritablePath(req.To)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if err := os.Rename(from, to); err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	writeJSON(w, map[string]string{"status": "renamed", "path": to})
}

func (a *filesApp) deleteFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req writePathRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	path, err := a.cleanWritablePath(req.Path)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			writeError(w, err, statusFor(err))
			return
		}
		if len(entries) > 0 {
			writeError(w, fmt.Errorf("refusing to delete non-empty directory"), http.StatusBadRequest)
			return
		}
	}
	if err := os.Remove(path); err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (a *filesApp) copyFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req copyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	from, err := cleanAnyPath(req.Path)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	to, err := a.cleanWritablePath(req.To)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if err := copyOneFile(from, to); err != nil {
		writeError(w, err, statusFor(err))
		return
	}
	writeJSON(w, map[string]string{"status": "copied", "path": to})
}

func (a *filesApp) discoverImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	roots := []string{a.workspace, "/"}
	items := discoverDiskImages(roots, 500)
	writeJSON(w, items)
}

func (a *filesApp) imageInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path, err := cleanAnyPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	info, err := qemuImgInfo(r.Context(), path)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	layers, _ := backingChain(r.Context(), path)
	writeJSON(w, imageInfoResponse{Info: info, Layers: layers})
}

func (a *filesApp) imageLayers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path, err := cleanAnyPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	layers, err := backingChain(r.Context(), path)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, layers)
}

func (a *filesApp) mountImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req mountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	image, err := cleanAnyPath(req.Path)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if err := a.diskMayBeMounted(image); err != nil {
		writeError(w, err, http.StatusConflict)
		return
	}

	a.mu.Lock()
	for _, session := range a.mounts {
		if session.Image == image {
			session.LastUsed = time.Now()
			copy := *session
			a.mu.Unlock()
			writeJSON(w, copy)
			return
		}
	}
	a.mu.Unlock()

	id := mountID(image)
	mountPoint := filepath.Join(runtimeMountRoot(), id)
	res, err := a.runPrivilegedHelper(r.Context(), helperRequest{
		Action:     "mount",
		Image:      image,
		MountPoint: mountPoint,
	})
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	now := time.Now()
	session := &mountSession{
		ID:        id,
		Image:     image,
		Path:      res.MountPoint,
		NBD:       res.NBD,
		MountedAt: now,
		LastUsed:  now,
		ReadOnly:  true,
	}
	a.mu.Lock()
	a.mounts[id] = session
	a.mu.Unlock()
	writeJSON(w, session)
}

func (a *filesApp) unmountImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req unmountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if err := a.unmountByID(r.Context(), req.ID); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "unmounted"})
}

func (a *filesApp) layerDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	upper, err := cleanAnyPath(r.URL.Query().Get("upper"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	lower, err := cleanAnyPath(r.URL.Query().Get("lower"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	up, err := qemuImgInfo(r.Context(), upper)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	lo, err := qemuImgInfo(r.Context(), lower)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"upper":   up,
		"lower":   lo,
		"message": "file-level layer diff requires both images to be mounted; v1 exposes backing metadata first",
	})
}

func (a *filesApp) staticFile(w http.ResponseWriter, r *http.Request) {
	serveStaticDir(w, r, a.static)
}

func (a *filesApp) runPrivilegedHelper(ctx context.Context, req helperRequest) (helperResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()

	payload, err := json.Marshal(req)
	if err != nil {
		return helperResponse{}, err
	}
	cmd := exec.CommandContext(ctx, "pkexec", a.helper, "helper")
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return helperResponse{}, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	var res helperResponse
	if err := json.Unmarshal(out, &res); err != nil {
		return helperResponse{}, fmt.Errorf("parse helper response: %w", err)
	}
	return res, nil
}

func (a *filesApp) cleanWritablePath(path string) (string, error) {
	clean, err := cleanAnyPath(path)
	if err != nil {
		return "", err
	}
	if a.pathReadOnly(clean) {
		return "", fmt.Errorf("%s is read-only", clean)
	}
	return clean, nil
}

func (a *filesApp) pathReadOnly(path string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, session := range a.mounts {
		if path == session.Path || strings.HasPrefix(path, session.Path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (a *filesApp) touchMount(path string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, session := range a.mounts {
		if path == session.Path || strings.HasPrefix(path, session.Path+string(filepath.Separator)) {
			session.LastUsed = time.Now()
		}
	}
}

func (a *filesApp) mountList() []*mountSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*mountSession, 0, len(a.mounts))
	for _, item := range a.mounts {
		copy := *item
		out = append(out, &copy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Image < out[j].Image })
	return out
}

func (a *filesApp) unmountByID(ctx context.Context, id string) error {
	a.mu.Lock()
	session := a.mounts[id]
	if session != nil {
		delete(a.mounts, id)
	}
	a.mu.Unlock()
	if session == nil {
		return fmt.Errorf("mount %q not found", id)
	}
	_, err := a.runPrivilegedHelper(ctx, helperRequest{
		Action:     "unmount",
		MountPoint: session.Path,
		NBD:        session.NBD,
	})
	return err
}

func (a *filesApp) cleanupAll(ctx context.Context) {
	for _, session := range a.mountList() {
		_ = a.unmountByID(ctx, session.ID)
	}
}

func (a *filesApp) reapIdleMounts() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		for _, session := range a.mountList() {
			if now.Sub(session.LastUsed) > idleUnmount {
				_ = a.unmountByID(context.Background(), session.ID)
			}
		}
	}
}

func (a *filesApp) diskMayBeMounted(path string) error {
	running, err := runningDomainDiskPaths(context.Background(), a.libvirt)
	if err != nil {
		return nil
	}
	for _, item := range running {
		if sameFilePath(item, path) {
			return fmt.Errorf("disk is attached to a running VM: %s", path)
		}
	}
	return nil
}

func listDirectory(path string, readOnly bool) ([]fileEntry, error) {
	items, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]fileEntry, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			continue
		}
		kind := "file"
		if info.IsDir() {
			kind = "dir"
		}
		out = append(out, fileEntry{
			Name:     item.Name(),
			Path:     filepath.Join(path, item.Name()),
			Type:     kind,
			Size:     info.Size(),
			Mode:     info.Mode().String(),
			Modified: info.ModTime().Format(time.RFC3339),
			ReadOnly: readOnly,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type == "dir"
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func cleanAnyPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute")
	}
	return filepath.Clean(path), nil
}

func parentPath(path string) string {
	parent := filepath.Dir(path)
	if parent == "." {
		return "/"
	}
	return parent
}

func copyOneFile(src, dest string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("copying directories is not supported in v1")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func discoverDiskImages(roots []string, limit int) []imageSummary {
	seen := map[string]bool{}
	out := []imageSummary{}
	for _, root := range roots {
		if len(out) >= limit {
			break
		}
		root = filepath.Clean(root)
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || len(out) >= limit {
				return nil
			}
			if d.IsDir() {
				if shouldSkipScanDir(path) {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".qcow2" && ext != ".raw" && ext != ".img" {
				return nil
			}
			clean := filepath.Clean(path)
			if seen[clean] {
				return nil
			}
			seen[clean] = true
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out = append(out, imageSummary{
				Path:     clean,
				Name:     filepath.Base(clean),
				Size:     info.Size(),
				Modified: info.ModTime().Format(time.RFC3339),
			})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func shouldSkipScanDir(path string) bool {
	clean := filepath.Clean(path)
	if clean == "/proc" || clean == "/sys" || clean == "/dev" || clean == "/run" || clean == "/tmp" {
		return true
	}
	base := filepath.Base(clean)
	return base == ".git" || base == "node_modules" || base == "dist" || base == "build"
}

func qemuImgInfo(ctx context.Context, path string) (qemuInfo, error) {
	cmd := qemuImgInfoCommand(ctx, path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return qemuInfo{}, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	var info qemuInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return qemuInfo{}, err
	}
	if info.Filename == "" {
		info.Filename = path
	}
	return info, nil
}

func qemuImgInfoCommand(ctx context.Context, path string) *exec.Cmd {
	return exec.CommandContext(ctx, "qemu-img", "info", "-U", "--output=json", path)
}

func backingChain(ctx context.Context, path string) ([]layerInfo, error) {
	var out []layerInfo
	seen := map[string]bool{}
	current := path
	for current != "" {
		current = filepath.Clean(current)
		if seen[current] {
			return nil, fmt.Errorf("backing chain loop at %s", current)
		}
		seen[current] = true
		info, err := qemuImgInfo(ctx, current)
		if err != nil {
			return nil, err
		}
		backing := info.FullBackingFilename
		if backing == "" {
			backing = info.BackingFilename
			if backing != "" && !filepath.IsAbs(backing) {
				backing = filepath.Join(filepath.Dir(current), backing)
			}
		}
		out = append(out, layerInfo{
			Path:        current,
			Format:      info.Format,
			VirtualSize: info.VirtualSize,
			ActualSize:  info.ActualSize,
			Backing:     backing,
		})
		current = backing
	}
	return out, nil
}

func runningDomainDiskPaths(ctx context.Context, uri string) ([]string, error) {
	if _, err := exec.LookPath("virsh"); err != nil {
		return nil, err
	}
	args := []string{}
	if uri != "" {
		args = append(args, "-c", uri)
	}
	args = append(args, "list", "--name", "--state-running")
	out, err := exec.CommandContext(ctx, "virsh", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	var paths []string
	for _, name := range strings.Fields(string(out)) {
		listArgs := []string{}
		if uri != "" {
			listArgs = append(listArgs, "-c", uri)
		}
		listArgs = append(listArgs, "domblklist", "--details", name)
		blk, err := exec.CommandContext(ctx, "virsh", listArgs...).CombinedOutput()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(blk), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 4 && filepath.IsAbs(fields[len(fields)-1]) {
				paths = append(paths, filepath.Clean(fields[len(fields)-1]))
			}
		}
	}
	return paths, nil
}

func sameFilePath(a, b string) bool {
	aa, errA := filepath.EvalSymlinks(a)
	bb, errB := filepath.EvalSymlinks(b)
	if errA == nil {
		a = aa
	}
	if errB == nil {
		b = bb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func runHelper(in io.Reader, out io.Writer) error {
	var req helperRequest
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	var res helperResponse
	var err error
	switch req.Action {
	case "mount":
		res, err = helperMount(req)
	case "unmount":
		res, err = helperUnmount(req)
	default:
		err = fmt.Errorf("unsupported helper action %q", req.Action)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(res)
}

func helperMount(req helperRequest) (helperResponse, error) {
	image, err := cleanAnyPath(req.Image)
	if err != nil {
		return helperResponse{}, err
	}
	mountPoint, err := cleanAnyPath(req.MountPoint)
	if err != nil {
		return helperResponse{}, err
	}
	if !insideRuntimeMountRoot(mountPoint) {
		return helperResponse{}, fmt.Errorf("mount point is outside foxlab runtime root")
	}
	if _, err := os.Stat(image); err != nil {
		return helperResponse{}, err
	}
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		return helperResponse{}, err
	}
	_ = exec.Command("modprobe", "nbd", "max_part=16").Run()
	nbd, err := freeNBD()
	if err != nil {
		return helperResponse{}, err
	}
	if out, err := exec.Command("qemu-nbd", "--read-only", "--connect", nbd, image).CombinedOutput(); err != nil {
		return helperResponse{}, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	_ = exec.Command("udevadm", "settle").Run()
	source := mountedBlockSource(nbd)
	if out, err := exec.Command("mount", "-o", "ro", source, mountPoint).CombinedOutput(); err != nil {
		_ = exec.Command("qemu-nbd", "--disconnect", nbd).Run()
		return helperResponse{}, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return helperResponse{MountPoint: mountPoint, NBD: nbd}, nil
}

func helperUnmount(req helperRequest) (helperResponse, error) {
	mountPoint, err := cleanAnyPath(req.MountPoint)
	if err != nil {
		return helperResponse{}, err
	}
	if !insideRuntimeMountRoot(mountPoint) {
		return helperResponse{}, fmt.Errorf("mount point is outside foxlab runtime root")
	}
	if mountPoint != "" {
		if out, err := exec.Command("umount", mountPoint).CombinedOutput(); err != nil {
			return helperResponse{}, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
		}
		_ = os.Remove(mountPoint)
	}
	if req.NBD != "" {
		nbd, err := cleanNBD(req.NBD)
		if err != nil {
			return helperResponse{}, err
		}
		if out, err := exec.Command("qemu-nbd", "--disconnect", nbd).CombinedOutput(); err != nil {
			return helperResponse{}, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	return helperResponse{}, nil
}

func freeNBD() (string, error) {
	for i := 0; i < 32; i++ {
		path := fmt.Sprintf("/dev/nbd%d", i)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		out, _ := os.ReadFile(filepath.Join("/sys/block", fmt.Sprintf("nbd%d", i), "size"))
		if strings.TrimSpace(string(out)) == "0" {
			return path, nil
		}
	}
	return "", fmt.Errorf("no free /dev/nbd device found")
}

func mountedBlockSource(nbd string) string {
	for i := 1; i <= 16; i++ {
		part := fmt.Sprintf("%sp%d", nbd, i)
		if _, err := os.Stat(part); err == nil {
			return part
		}
	}
	return nbd
}

func cleanNBD(path string) (string, error) {
	if !strings.HasPrefix(path, "/dev/nbd") {
		return "", fmt.Errorf("invalid nbd path")
	}
	clean := filepath.Clean(path)
	if _, err := strconv.Atoi(strings.TrimPrefix(clean, "/dev/nbd")); err != nil {
		return "", fmt.Errorf("invalid nbd path")
	}
	return clean, nil
}

func runtimeMountRoot() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "foxlab", "files", "mounts")
	}
	return filepath.Join(os.TempDir(), "foxlab", "files", "mounts")
}

func insideRuntimeMountRoot(path string) bool {
	for _, root := range allowedRuntimeMountRoots() {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func allowedRuntimeMountRoots() []string {
	roots := []string{runtimeMountRoot(), filepath.Join(os.TempDir(), "foxlab", "files", "mounts")}
	for _, entry := range []string{"/run/user", "/var/run/user"} {
		items, err := os.ReadDir(entry)
		if err != nil {
			continue
		}
		for _, item := range items {
			if !item.IsDir() {
				continue
			}
			if _, err := strconv.Atoi(item.Name()); err != nil {
				continue
			}
			roots = append(roots, filepath.Join(entry, item.Name(), "foxlab", "files", "mounts"))
		}
	}
	return roots
}

func mountID(path string) string {
	sum := sha1.Sum([]byte(filepath.Clean(path)))
	return hex.EncodeToString(sum[:8])
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1024*1024))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func cleanWorkspace(workspace string) string {
	if workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return workspace
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return abs
	}
	home, err := os.UserHomeDir()
	if err == nil {
		return home
	}
	return "."
}

func normalizeWindowTarget(appAddr string, targetHost *string, targetPort *string, targetPath *string) {
	host, port, err := net.SplitHostPort(appAddr)
	if err != nil {
		return
	}
	if host == "" || host == "localhost" {
		host = "127.0.0.1"
	}
	*targetHost = host
	*targetPort = port
	if *targetPath == "" {
		*targetPath = "/"
	}
	if !strings.HasPrefix(*targetPath, "/") {
		*targetPath = "/" + *targetPath
	}
}

func notifyWMWhenReady(wmAddr string, request wm.OpenWindowRequest) {
	if request.Host == "" || request.Port == "" {
		log.Printf("wm open-window skipped: invalid window target")
		return
	}
	healthURL := "http://" + net.JoinHostPort(request.Host, request.Port) + "/healthz"
	client := http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := client.Get(healthURL)
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := wm.OpenWindow(ctx, wmAddr, request); err != nil {
					log.Printf("wm open-window failed: %v", err)
				}
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("wm open-window skipped: files did not become ready")
}

func serveUntilInterrupted(srv *http.Server) {
	errc := make(chan error, 1)
	go func() {
		errc <- srv.ListenAndServe()
	}()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)
	defer signal.Stop(sigc)
	select {
	case err := <-errc:
		if err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	case <-sigc:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Fatal(err)
		}
	}
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
	if errors.Is(err, os.ErrPermission) {
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}
