package foxapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	FormatV1     = "foxapp.v1"
	ManifestFile = "foxapp.json"
)

type Manifest struct {
	Format  string      `json:"format"`
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Run     CommandSpec `json:"run"`
	Icon    IconSpec    `json:"icon"`
	Window  WindowSpec  `json:"window"`
	Health  HealthSpec  `json:"health"`
	Web     WebSpec     `json:"web"`
}

type CommandSpec struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type IconSpec struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type WindowSpec struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

type HealthSpec struct {
	Path string `json:"path"`
}

type WebSpec struct {
	Dist string `json:"dist"`
}

func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func LoadManifestDir(dir string) (*Manifest, error) {
	return LoadManifest(filepath.Join(dir, ManifestFile))
}

func (m *Manifest) Validate() error {
	if m.Format != FormatV1 {
		return fmt.Errorf("unsupported fox app format %q", m.Format)
	}
	if m.ID == "" {
		return fmt.Errorf("app id is required")
	}
	if strings.ContainsAny(m.ID, `/\`) {
		return fmt.Errorf("app id must not contain path separators")
	}
	if m.Name == "" {
		return fmt.Errorf("app name is required")
	}
	if m.Version == "" {
		return fmt.Errorf("app version is required")
	}
	if m.Run.Command == "" {
		return fmt.Errorf("run.command is required")
	}
	if m.Icon.Type == "" || m.Icon.Value == "" {
		return fmt.Errorf("icon.type and icon.value are required")
	}
	if m.Window.Title == "" {
		return fmt.Errorf("window.title is required")
	}
	if m.Window.Path == "" {
		return fmt.Errorf("window.path is required")
	}
	if !strings.HasPrefix(m.Window.Path, "/") {
		return fmt.Errorf("window.path must start with /")
	}
	if m.Health.Path == "" {
		return fmt.Errorf("health.path is required")
	}
	if !strings.HasPrefix(m.Health.Path, "/") {
		return fmt.Errorf("health.path must start with /")
	}
	if m.Web.Dist == "" {
		return fmt.Errorf("web.dist is required")
	}
	if filepath.IsAbs(m.Run.Command) || filepath.IsAbs(m.Web.Dist) {
		return fmt.Errorf("package paths must be relative")
	}
	return nil
}
