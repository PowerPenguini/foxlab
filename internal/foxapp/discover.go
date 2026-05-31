package foxapp

import (
	"os"
	"path/filepath"
	"sort"
)

type PackageRef struct {
	Path     string
	Manifest *Manifest
}

func DefaultAppDirs() []string {
	dirs := filepath.SplitList(os.Getenv("FOXLAB_APP_DIRS"))
	if userDir := UserAppDir(); userDir != "" {
		dirs = append(dirs, userDir)
	}
	dirs = append(dirs, filepath.Join("dist", "apps"))
	return compactStrings(dirs)
}

func UserAppDir() string {
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "foxlab", "apps")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "foxlab", "apps")
}

func DiscoverPackages(dirs []string) ([]PackageRef, error) {
	var refs []PackageRef
	for _, dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*.foxapp"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, path := range matches {
			manifest, err := Inspect(path)
			if err != nil {
				continue
			}
			refs = append(refs, PackageRef{Path: path, Manifest: manifest})
		}
	}
	return refs, nil
}

func compactStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
