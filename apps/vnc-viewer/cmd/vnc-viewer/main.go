package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"foxlab/internal/vncproxy"
	"foxlab/internal/wm"
)

func main() {
	fs := flag.NewFlagSet("vnc-viewer", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8091", "HTTP listen address")
	workspace := fs.String("workspace", ".", "workspace directory")
	uri := fs.String("uri", "", "libvirt URI")
	wmAddr := fs.String("wm-addr", "", "window manager gRPC address")
	wmAppID := fs.String("wm-app-id", "vnc-viewer", "window manager app id")
	wmName := fs.String("wm-name", "VNC Viewer", "window manager app name")
	wmTitle := fs.String("wm-title", "VNC Viewer", "window manager window title")
	wmIconType := fs.String("wm-icon-type", "builtin", "window manager icon type")
	wmIconValue := fs.String("wm-icon-value", "monitor", "window manager icon value")
	wmPath := fs.String("wm-path", "/", "path to open in the window")
	vncHost := fs.String("vnc-host", "", "target VNC host")
	vncPort := fs.Int("vnc-port", 0, "target VNC port")
	vmLabel := fs.String("vm-label", "", "VM label for display")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: vnc-viewer --vnc-host 127.0.0.1 --vnc-port 5900 [--wm-addr 127.0.0.1:12345]")
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])
	_ = workspace
	_ = uri

	app := &viewerApp{
		vncHost: *vncHost,
		vncPort: *vncPort,
		vmLabel: *vmLabel,
		static:  filepath.Join("web", "dist"),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", app.health)
	mux.HandleFunc("/api/console", app.console)
	mux.HandleFunc("/api/vnc/ws", app.vncWebsocket)
	mux.HandleFunc("/", app.staticFile)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("Foxlab VNC viewer listening at http://%s\n", *addr)
	if *wmAddr != "" {
		go notifyWMWhenReady(*wmAddr, *addr, wm.OpenWindowRequest{
			AppID: *wmAppID,
			Name:  *wmName,
			Title: *wmTitle,
			Icon:  wm.Icon{Type: *wmIconType, Value: *wmIconValue},
			Path:  *wmPath,
		})
	}
	serveUntilInterrupted(srv)
}

type viewerApp struct {
	vncHost string
	vncPort int
	vmLabel string
	static  string
}

func (a *viewerApp) health(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (a *viewerApp) console(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/console" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.vncHost == "" || a.vncPort <= 0 {
		writeError(w, fmt.Errorf("VNC target is not configured"), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"enabled": true,
		"path":    "/api/vnc/ws",
		"label":   a.vmLabel,
	})
}

func (a *viewerApp) vncWebsocket(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/vnc/ws" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.vncHost == "" || a.vncPort <= 0 {
		writeError(w, fmt.Errorf("VNC target is not configured"), http.StatusBadRequest)
		return
	}
	vncproxy.ProxyRawTCP(w, r, net.JoinHostPort(a.vncHost, strconv.Itoa(a.vncPort)))
}

func (a *viewerApp) staticFile(w http.ResponseWriter, r *http.Request) {
	serveStaticDir(w, r, a.static)
}

func notifyWMWhenReady(wmAddr, appAddr string, request wm.OpenWindowRequest) {
	host, port, err := net.SplitHostPort(appAddr)
	if err != nil {
		log.Printf("wm open-window skipped: invalid app address %q: %v", appAddr, err)
		return
	}
	if host == "" || host == "localhost" {
		host = "127.0.0.1"
	}
	if request.Path == "" {
		request.Path = "/"
	}
	if !strings.HasPrefix(request.Path, "/") {
		request.Path = "/" + request.Path
	}
	request.Host = host
	request.Port = port

	healthURL := "http://" + net.JoinHostPort(host, port) + "/healthz"
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
	log.Printf("wm open-window skipped: VNC viewer did not become ready")
}

func serveUntilInterrupted(srv *http.Server) {
	errc := make(chan error, 1)
	go func() {
		errc <- srv.ListenAndServe()
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
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
