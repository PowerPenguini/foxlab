package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"foxlab/internal/server"
	"foxlab/internal/wm"
)

const defaultLibvirtURI = "qemu:///system"

func main() {
	fs := flag.NewFlagSet("topology", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8090", "HTTP listen address")
	workspace := fs.String("workspace", ".", "directory containing lab YAML files")
	uri := fs.String("uri", defaultLibvirtURI, "libvirt URI")
	wmAddr := fs.String("wm-addr", "", "window manager gRPC address")
	wmAppID := fs.String("wm-app-id", "topology", "window manager app id")
	wmName := fs.String("wm-name", "Topology Editor", "window manager app name")
	wmTitle := fs.String("wm-title", "Topology editor", "window manager window title")
	wmIconType := fs.String("wm-icon-type", "builtin", "window manager icon type")
	wmIconValue := fs.String("wm-icon-value", "network", "window manager icon value")
	wmPath := fs.String("wm-path", "/", "path to open in the window")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: topology [--addr 127.0.0.1:8090] [--workspace .] [--uri qemu:///system] [--wm-addr 127.0.0.1:12345]")
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])

	srv := server.NewTopology(server.Config{
		Addr:       *addr,
		Workspace:  *workspace,
		LibvirtURI: *uri,
		StaticDir:  "web/dist",
		WMAddr:     *wmAddr,
	})
	fmt.Printf("Foxlab topology app listening at http://%s\n", *addr)
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
	log.Printf("wm open-window skipped: topology app did not become ready")
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
