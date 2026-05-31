package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"foxlab/internal/disk"
	"foxlab/internal/foxapp"
	"foxlab/internal/lab"
	"foxlab/internal/server"
	"foxlab/internal/virt"
)

const (
	defaultLibvirtURI        = "qemu:///system"
	defaultDesktopLibvirtURI = "qemu:///system"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "validate":
		runValidate(os.Args[2:])
	case "apply":
		runApply(os.Args[2:])
	case "destroy":
		runDestroy(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	case "app":
		runApp(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func runValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: foxlab validate <lab.yaml>")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	path := requiredLabPath(fs)

	loaded, err := lab.LoadFile(path)
	if err != nil {
		log.Fatalf("validation failed: %v", err)
	}
	fmt.Printf("%s is valid (%d VM(s), %d switch(es), %d disk(s))\n", path, len(loaded.VMs), len(loaded.Switches), len(loaded.Disks))
}

func runApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	uri := fs.String("uri", defaultLibvirtURI, "libvirt URI")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: foxlab apply [--uri qemu:///system] <lab.yaml>")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	path := requiredLabPath(fs)

	loaded, err := lab.LoadFile(path)
	if err != nil {
		log.Fatalf("cannot load lab: %v", err)
	}
	manager := disk.NewManager()
	if err := manager.EnsureDeclaredDisks(context.Background(), loaded); err != nil {
		log.Fatalf("disk preparation failed: %v", err)
	}
	driver, err := virt.NewLibvirtDriver(*uri)
	if err != nil {
		log.Fatalf("libvirt connection failed: %v", err)
	}
	defer driver.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := driver.Apply(ctx, loaded); err != nil {
		log.Fatalf("apply failed: %v", err)
	}
	fmt.Printf("Applied lab %q\n", loaded.ID)
}

func runDestroy(args []string) {
	fs := flag.NewFlagSet("destroy", flag.ExitOnError)
	uri := fs.String("uri", defaultLibvirtURI, "libvirt URI")
	deleteDisks := fs.Bool("delete-disks", false, "also delete Foxlab-managed disk files")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: foxlab destroy [--uri qemu:///system] [--delete-disks] <lab.yaml>")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	path := requiredLabPath(fs)

	loaded, err := lab.LoadFile(path)
	if err != nil {
		log.Fatalf("cannot load lab: %v", err)
	}
	driver, err := virt.NewLibvirtDriver(*uri)
	if err != nil {
		log.Fatalf("libvirt connection failed: %v", err)
	}
	defer driver.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := driver.Destroy(ctx, loaded); err != nil {
		log.Fatalf("destroy failed: %v", err)
	}
	if *deleteDisks {
		if err := disk.NewManager().DeleteDeclaredDisks(loaded); err != nil {
			log.Fatalf("disk deletion failed: %v", err)
		}
	}
	fmt.Printf("Destroyed lab %q\n", loaded.ID)
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	uri := fs.String("uri", defaultLibvirtURI, "libvirt URI")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: foxlab status [--uri qemu:///system] <lab.yaml>")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	path := requiredLabPath(fs)

	loaded, err := lab.LoadFile(path)
	if err != nil {
		log.Fatalf("cannot load lab: %v", err)
	}
	driver, err := virt.NewLibvirtDriver(*uri)
	if err != nil {
		log.Fatalf("libvirt connection failed: %v", err)
	}
	defer driver.Close()

	status, err := driver.Status(context.Background(), loaded)
	if err != nil {
		log.Fatalf("status failed: %v", err)
	}
	for _, sw := range status.Switches {
		fmt.Printf("switch %-24s %s\n", sw.ID, sw.State)
	}
	for _, vm := range status.VMs {
		if vm.Console.Port > 0 {
			fmt.Printf("vm     %-24s %-10s vnc=127.0.0.1:%d\n", vm.ID, vm.State, vm.Console.Port)
			continue
		}
		fmt.Printf("vm     %-24s %s\n", vm.ID, vm.State)
	}
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8088", "HTTP listen address")
	workspace := fs.String("workspace", ".", "directory containing lab YAML files")
	uri := fs.String("uri", defaultDesktopLibvirtURI, "libvirt URI")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: foxlab serve [--addr 127.0.0.1:8088] [--workspace .] [--uri qemu:///system]")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	srv := server.New(server.Config{
		Addr:       *addr,
		Workspace:  *workspace,
		LibvirtURI: *uri,
	})
	fmt.Printf("Foxlab shell listening at http://%s\n", *addr)
	serveUntilInterrupted(srv)
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

func runApp(args []string) {
	if len(args) == 0 {
		appUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "package":
		runAppPackage(args[1:])
	case "inspect":
		runAppInspect(args[1:])
	case "run":
		runAppRun(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown app command %q\n\n", args[0])
		appUsage()
		os.Exit(2)
	}
}

func runAppPackage(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: foxlab app package <app-dir> --out <app.foxapp>")
		os.Exit(2)
	}
	appDir := args[0]
	fs := flag.NewFlagSet("app package", flag.ExitOnError)
	out := fs.String("out", "", "output .foxapp archive path")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: foxlab app package <app-dir> --out <app.foxapp>")
		fs.PrintDefaults()
	}
	fs.Parse(args[1:])
	if *out == "" {
		fs.Usage()
		os.Exit(2)
	}
	if err := foxapp.Package(appDir, *out); err != nil {
		log.Fatalf("package failed: %v", err)
	}
	fmt.Printf("Packaged %s\n", *out)
}

func runAppInspect(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: foxlab app inspect <app.foxapp>")
		os.Exit(2)
	}
	manifest, err := foxapp.Inspect(args[0])
	if err != nil {
		log.Fatalf("inspect failed: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(manifest); err != nil {
		log.Fatal(err)
	}
}

func runAppRun(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: foxlab app run <app.foxapp> [--addr 127.0.0.1:8090] [--workspace .] [--uri qemu:///system] [--wm-addr 127.0.0.1:12345] [-- app-args...]")
		os.Exit(2)
	}
	packagePath := args[0]
	fs := flag.NewFlagSet("app run", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8090", "HTTP listen address")
	workspace := fs.String("workspace", ".", "directory containing lab YAML files")
	uri := fs.String("uri", defaultDesktopLibvirtURI, "libvirt URI")
	wmAddr := fs.String("wm-addr", "", "window manager gRPC address")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: foxlab app run <app.foxapp> [--addr 127.0.0.1:8090] [--workspace .] [--uri qemu:///system] [--wm-addr 127.0.0.1:12345] [-- app-args...]")
		fs.PrintDefaults()
	}
	fs.Parse(args[1:])

	extractDir, err := os.MkdirTemp("", "foxapp-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(extractDir)
	if err := foxapp.Extract(packagePath, extractDir); err != nil {
		log.Fatalf("cannot extract app: %v", err)
	}
	manifest, err := foxapp.LoadManifestDir(extractDir)
	if err != nil {
		log.Fatalf("cannot load app: %v", err)
	}
	runtime := foxapp.Runtime{
		Addr:       *addr,
		Workspace:  *workspace,
		LibvirtURI: *uri,
		WMAddr:     *wmAddr,
		ExtraArgs:  fs.Args(),
	}
	cmd := foxapp.PackageCommand(extractDir, manifest, runtime)
	fmt.Printf("Running %s at http://%s\n", manifest.Name, *addr)
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	waitForCommandOrInterrupt(cmd)
}

func waitForCommandOrInterrupt(cmd *exec.Cmd) {
	errc := make(chan error, 1)
	go func() {
		errc <- cmd.Wait()
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)
	defer signal.Stop(sigc)

	select {
	case err := <-errc:
		if err != nil {
			log.Fatal(err)
		}
	case <-sigc:
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		if err := <-errc; err != nil {
			log.Fatal(err)
		}
	}
}

func requiredLabPath(fs *flag.FlagSet) string {
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	return fs.Arg(0)
}

func usage() {
	fmt.Fprintln(os.Stderr, `foxlab - declarative local virtual lab builder

Commands:
  validate <lab.yaml>       parse and validate a lab file
  apply <lab.yaml>          create/update managed libvirt networks and VMs
  destroy <lab.yaml>        stop and remove managed libvirt networks and VMs
  status <lab.yaml>         show managed resource state
  serve                     run the local desktop shell
  app package <dir>         build a .foxapp archive
  app inspect <app.foxapp>  print package metadata
  app run <app.foxapp>      run a packaged app

Run "foxlab <command> -h" for command options.`)
}

func appUsage() {
	fmt.Fprintln(os.Stderr, `foxlab app - manage Fox apps

Commands:
  package <dir> --out <app.foxapp>
  inspect <app.foxapp>
  run <app.foxapp>`)
}
