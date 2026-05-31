package disk

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"foxlab/internal/lab"
)

type Manager struct{}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) EnsureDeclaredDisks(ctx context.Context, l *lab.Lab) error {
	for _, d := range l.Disks {
		dest := l.ResolveDiskPath(d.Path)
		if _, err := os.Stat(dest); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if d.Source != "" {
			if err := copyFile(l.ResolvePath(d.Source), dest); err != nil {
				return fmt.Errorf("import disk %q: %w", d.ID, err)
			}
			continue
		}
		if d.SizeGB <= 0 {
			return fmt.Errorf("disk %q needs sizeGB when source is not set", d.ID)
		}
		if err := qemuImgCreate(ctx, dest, d.Format, d.SizeGB); err != nil {
			return fmt.Errorf("create disk %q: %w", d.ID, err)
		}
	}
	return nil
}

func (m *Manager) DeleteDeclaredDisks(l *lab.Lab) error {
	for _, d := range l.Disks {
		path := l.ResolveDiskPath(d.Path)
		if !inside(l.Root(), path) {
			return fmt.Errorf("refusing to delete disk %q outside lab workspace", d.ID)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (m *Manager) CreateDisk(ctx context.Context, l *lab.Lab, d lab.Disk) error {
	path := l.ResolveDiskPath(d.Path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if d.Source != "" {
		return copyFile(l.ResolvePath(d.Source), path)
	}
	return qemuImgCreate(ctx, path, d.Format, d.SizeGB)
}

func (m *Manager) DeleteDisk(l *lab.Lab, d lab.Disk) error {
	path := l.ResolveDiskPath(d.Path)
	if !inside(l.Root(), path) {
		return fmt.Errorf("refusing to delete disk %q outside lab workspace", d.ID)
	}
	return os.Remove(path)
}

func qemuImgCreate(ctx context.Context, path, format string, sizeGB int) error {
	if format == "" {
		format = "qcow2"
	}
	if sizeGB <= 0 {
		return fmt.Errorf("sizeGB must be greater than zero")
	}
	cmd := exec.CommandContext(ctx, "qemu-img", "create", "-f", format, path, fmt.Sprintf("%dG", sizeGB))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", string(out), err)
	}
	return nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func inside(root, path string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
