package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	libvirt "github.com/libvirt/libvirt-go"

	"foxlab/internal/wm"
)

const (
	defaultRows = 24
	defaultCols = 80
)

func main() {
	fs := flag.NewFlagSet("terminal", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8092", "HTTP listen address")
	workspace := fs.String("workspace", ".", "workspace directory")
	uri := fs.String("uri", "", "libvirt URI")
	command := fs.String("command", "", "command to run instead of the default shell")
	consoleDomain := fs.String("libvirt-console-domain", "", "libvirt domain name for serial console mode")
	wmAddr := fs.String("wm-addr", "", "window manager gRPC address")
	wmAppID := fs.String("wm-app-id", "terminal", "window manager app id")
	wmName := fs.String("wm-name", "Terminal", "window manager app name")
	wmTitle := fs.String("wm-title", "Terminal", "window manager window title")
	wmIconType := fs.String("wm-icon-type", "builtin", "window manager icon type")
	wmIconValue := fs.String("wm-icon-value", "terminal", "window manager icon value")
	wmPath := fs.String("wm-path", "/", "path to open in the window")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: terminal [--addr 127.0.0.1:8092] [--workspace .] [--wm-addr 127.0.0.1:12345]")
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])

	app := &terminalApp{
		workspace:     cleanWorkspace(*workspace),
		shell:         defaultShell(),
		command:       *command,
		libvirtURI:    *uri,
		consoleDomain: *consoleDomain,
		static:        filepath.Join("web", "dist"),
		wmAddr:        *wmAddr,
		closeRequest: wm.CloseWindowRequest{
			AppID: *wmAppID,
			Path:  normalizedWindowPath(*wmPath),
		},
		shutdown: make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", app.health)
	mux.HandleFunc("/api/terminal", app.terminal)
	mux.HandleFunc("/api/terminal/ws", app.terminalWebsocket)
	mux.HandleFunc("/", app.staticFile)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("Foxlab terminal listening at http://%s\n", *addr)
	if *wmAddr != "" {
		openRequest := wm.OpenWindowRequest{
			AppID: *wmAppID,
			Name:  *wmName,
			Title: *wmTitle,
			Icon:  wm.Icon{Type: *wmIconType, Value: *wmIconValue},
			Path:  *wmPath,
		}
		normalizeWindowTarget(*addr, &openRequest.Host, &openRequest.Port, &openRequest.Path)
		app.closeRequest.Host = openRequest.Host
		app.closeRequest.Port = openRequest.Port
		app.closeRequest.Path = openRequest.Path
		go notifyWMWhenReady(*wmAddr, openRequest)
	}
	serveUntilInterrupted(srv, app.shutdown)
}

type terminalApp struct {
	workspace     string
	shell         string
	command       string
	libvirtURI    string
	consoleDomain string
	static        string
	wmAddr        string
	closeRequest  wm.CloseWindowRequest
	shutdown      chan struct{}
	shutdownOnce  sync.Once
}

type clientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

type serverMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Code int    `json:"code,omitempty"`
}

func (a *terminalApp) health(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (a *terminalApp) terminal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/terminal" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]string{
		"shell":     a.shell,
		"workspace": a.workspace,
		"path":      "/api/terminal/ws",
	})
}

func (a *terminalApp) terminalWebsocket(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/terminal/ws" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	client, reader, err := upgradeWebsocket(w, r)
	if err != nil {
		return
	}
	defer client.Close()
	if a.consoleDomain != "" {
		a.libvirtConsoleWebsocket(r.Context(), client, reader)
		return
	}

	cmd := terminalCommand(a.shell, a.command)
	cmd.Dir = a.workspace
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: defaultRows, Cols: defaultCols})
	if err != nil {
		writeWSJSON(client, serverMessage{Type: "error", Data: err.Error()})
		return
	}
	defer tty.Close()

	var writeMu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, err := tty.Read(buf)
			if n > 0 {
				writeMu.Lock()
				_ = writeWSJSON(client, serverMessage{Type: "output", Data: string(buf[:n])})
				writeMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			opcode, payload, err := readWebsocketFrame(reader)
			if err != nil {
				return
			}
			if opcode == 0x8 {
				return
			}
			if opcode != 0x1 && opcode != 0x2 && opcode != 0x0 {
				continue
			}
			var msg clientMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "input":
				if msg.Data != "" {
					_, _ = tty.Write([]byte(msg.Data))
				}
			case "resize":
				rows, cols := clampTerminalSize(msg.Rows, msg.Cols)
				_ = pty.Setsize(tty, &pty.Winsize{Rows: rows, Cols: cols})
			}
		}
	}()

	exit := make(chan error, 1)
	go func() {
		exit <- cmd.Wait()
	}()

	select {
	case err := <-exit:
		_ = tty.Close()
		<-done
		writeMu.Lock()
		_ = writeWSJSON(client, exitMessage(err))
		writeMu.Unlock()
		go a.closeWindowAndShutdown()
	case <-readDone:
		_ = tty.Close()
		select {
		case <-exit:
		case <-time.After(500 * time.Millisecond):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-exit
		}
	case <-r.Context().Done():
		_ = tty.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-exit
	}
}

func (a *terminalApp) libvirtConsoleWebsocket(ctx context.Context, client net.Conn, reader *bufio.Reader) {
	conn, err := libvirt.NewConnect(a.libvirtURI)
	if err != nil {
		writeWSJSON(client, serverMessage{Type: "error", Data: err.Error()})
		return
	}
	defer conn.Close()
	dom, err := conn.LookupDomainByName(a.consoleDomain)
	if err != nil {
		writeWSJSON(client, serverMessage{Type: "error", Data: err.Error()})
		return
	}
	defer dom.Free()
	stream, err := conn.NewStream(0)
	if err != nil {
		writeWSJSON(client, serverMessage{Type: "error", Data: err.Error()})
		return
	}
	defer stream.Free()
	if err := dom.OpenConsole("", stream, libvirt.DOMAIN_CONSOLE_FORCE); err != nil {
		writeWSJSON(client, serverMessage{Type: "error", Data: err.Error()})
		return
	}

	var writeMu sync.Mutex
	streamDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stream.Recv(buf)
			if n > 0 {
				writeMu.Lock()
				_ = writeWSJSON(client, serverMessage{Type: "output", Data: string(buf[:n])})
				writeMu.Unlock()
			}
			if err != nil {
				streamDone <- err
				return
			}
		}
	}()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			opcode, payload, err := readWebsocketFrame(reader)
			if err != nil {
				return
			}
			if opcode == 0x8 {
				return
			}
			if opcode != 0x1 && opcode != 0x2 && opcode != 0x0 {
				continue
			}
			var msg clientMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				continue
			}
			if msg.Type == "input" && msg.Data != "" {
				_, _ = stream.Send([]byte(msg.Data))
			}
		}
	}()

	select {
	case err := <-streamDone:
		writeMu.Lock()
		_ = writeWSJSON(client, exitMessage(err))
		writeMu.Unlock()
		go a.closeWindowAndShutdown()
	case <-readDone:
		_ = stream.Abort()
	case <-ctx.Done():
		_ = stream.Abort()
	}
}

func (a *terminalApp) closeWindowAndShutdown() {
	time.Sleep(80 * time.Millisecond)
	if a.wmAddr != "" && a.closeRequest.AppID != "" && a.closeRequest.Host != "" && a.closeRequest.Port != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := wm.CloseWindow(ctx, a.wmAddr, a.closeRequest); err != nil {
			log.Printf("wm close-window failed: %v", err)
		}
		cancel()
	}
	a.shutdownOnce.Do(func() {
		close(a.shutdown)
	})
}

func (a *terminalApp) staticFile(w http.ResponseWriter, r *http.Request) {
	serveStaticDir(w, r, a.static)
}

func defaultShell() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return "/bin/sh"
	}
	if _, err := os.Stat(shell); err == nil {
		return shell
	}
	return "/bin/sh"
}

func terminalCommand(shell, command string) *exec.Cmd {
	if command == "" {
		return exec.Command(shell)
	}
	return exec.Command(shell, "-lc", command)
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

func clampTerminalSize(rows, cols uint16) (uint16, uint16) {
	if rows < 2 {
		rows = defaultRows
	}
	if cols < 10 {
		cols = defaultCols
	}
	if rows > 300 {
		rows = 300
	}
	if cols > 500 {
		cols = 500
	}
	return rows, cols
}

func exitMessage(err error) serverMessage {
	if err == nil {
		return serverMessage{Type: "exit", Code: 0}
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return serverMessage{Type: "exit", Code: exitErr.ExitCode()}
	}
	return serverMessage{Type: "error", Data: err.Error(), Code: 1}
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
	*targetPath = normalizedWindowPath(*targetPath)
}

func normalizedWindowPath(path string) string {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
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
	log.Printf("wm open-window skipped: terminal did not become ready")
}

func serveUntilInterrupted(srv *http.Server, shutdown <-chan struct{}) {
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
	case <-shutdown:
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

func upgradeWebsocket(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.Reader, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		err := fmt.Errorf("terminal endpoint expects a websocket upgrade")
		writeError(w, err, http.StatusUpgradeRequired)
		return nil, nil, err
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		err := fmt.Errorf("server does not support hijacking")
		writeError(w, err, http.StatusInternalServerError)
		return nil, nil, err
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		err := fmt.Errorf("websocket key is missing")
		writeError(w, err, http.StatusBadRequest)
		return nil, nil, err
	}
	client, rw, err := h.Hijack()
	if err != nil {
		return nil, nil, err
	}
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = rw.WriteString("Upgrade: websocket\r\n")
	_, _ = rw.WriteString("Connection: Upgrade\r\n")
	_, _ = rw.WriteString("Sec-WebSocket-Accept: " + websocketAccept(key) + "\r\n\r\n")
	_ = rw.Flush()
	return client, rw.Reader, nil
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func readWebsocketFrame(r *bufio.Reader) (byte, []byte, error) {
	header, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	opcode := header & 0x0f
	lenByte, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	masked := lenByte&0x80 != 0
	payloadLen := uint64(lenByte & 0x7f)
	switch payloadLen {
	case 126:
		var raw [2]byte
		if _, err := io.ReadFull(r, raw[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = uint64(binary.BigEndian.Uint16(raw[:]))
	case 127:
		var raw [8]byte
		if _, err := io.ReadFull(r, raw[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = binary.BigEndian.Uint64(raw[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	if payloadLen > 16*1024*1024 {
		return 0, nil, fmt.Errorf("websocket frame too large")
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeWSJSON(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeWebsocketFrame(w, 0x1, payload)
}

func writeWebsocketFrame(w io.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(len(payload)))
	default:
		header = append(header, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(len(payload)))
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
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
