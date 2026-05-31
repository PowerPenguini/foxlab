package foxapp

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Runtime struct {
	Addr       string
	Workspace  string
	LibvirtURI string
	WMAddr     string
	WMName     string
	WMTitle    string
	WMPath     string
	ExtraArgs  []string
	Env        []string
}

func PackageCommand(appDir string, manifest *Manifest, runtime Runtime) *exec.Cmd {
	return command(appDir, manifest.Run, manifest, runtime)
}

func command(appDir string, spec CommandSpec, manifest *Manifest, runtime Runtime) *exec.Cmd {
	commandPath := spec.Command
	if hasPathSeparator(commandPath) && !filepath.IsAbs(commandPath) {
		commandPath = filepath.Join(appDir, commandPath)
	}
	args := append([]string{}, spec.Args...)
	args = append(args, runtimeArgs(manifest, runtime)...)
	args = append(args, runtime.ExtraArgs...)
	cmd := exec.Command(commandPath, args...)
	cmd.Dir = appDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), runtime.Env...)
	return cmd
}

func runtimeArgs(manifest *Manifest, runtime Runtime) []string {
	args := []string{}
	if runtime.Addr != "" {
		args = append(args, "--addr", runtime.Addr)
	}
	if runtime.Workspace != "" {
		if abs, err := filepath.Abs(runtime.Workspace); err == nil {
			runtime.Workspace = abs
		}
		args = append(args, "--workspace", runtime.Workspace)
	}
	if runtime.LibvirtURI != "" {
		args = append(args, "--uri", runtime.LibvirtURI)
	}
	if runtime.WMAddr != "" {
		name := manifest.Name
		if runtime.WMName != "" {
			name = runtime.WMName
		}
		title := manifest.Window.Title
		if runtime.WMTitle != "" {
			title = runtime.WMTitle
		}
		path := manifest.Window.Path
		if runtime.WMPath != "" {
			path = runtime.WMPath
		}
		args = append(
			args,
			"--wm-addr", runtime.WMAddr,
			"--wm-app-id", manifest.ID,
			"--wm-name", name,
			"--wm-title", title,
			"--wm-icon-type", manifest.Icon.Type,
			"--wm-icon-value", manifest.Icon.Value,
			"--wm-path", path,
		)
	}
	return args
}

func URLForAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func hasPathSeparator(path string) bool {
	return strings.Contains(path, "/") || strings.Contains(path, `\`)
}
