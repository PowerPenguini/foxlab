package foxapp

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func Package(appDir, outPath string) error {
	manifest, err := LoadManifestDir(appDir)
	if err != nil {
		return err
	}
	if err := requirePackageInputs(appDir, manifest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	if err := addFile(tw, filepath.Join(appDir, ManifestFile), ManifestFile); err != nil {
		return err
	}
	if err := addFile(tw, filepath.Join(appDir, manifest.Run.Command), manifest.Run.Command); err != nil {
		return err
	}
	webRoot := filepath.Join(appDir, manifest.Web.Dist)
	return filepath.WalkDir(webRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(appDir, path)
		if err != nil {
			return err
		}
		return addFile(tw, path, filepath.ToSlash(rel))
	})
}

func Inspect(packagePath string) (*Manifest, error) {
	file, err := os.Open(packagePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Name != ManifestFile {
			continue
		}
		var manifest Manifest
		if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
			return nil, err
		}
		if err := manifest.Validate(); err != nil {
			return nil, err
		}
		return &manifest, nil
	}
	return nil, fmt.Errorf("%s is missing %s", packagePath, ManifestFile)
}

func Extract(packagePath, destDir string) error {
	file, err := os.Open(packagePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target, err := safeArchivePath(destDir, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archive entry type for %s", header.Name)
		}
	}
	return nil
}

func safeArchivePath(root, name string) (string, error) {
	clean := filepath.Clean(name)
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid archive path %q", name)
	}
	return filepath.Join(root, clean), nil
}

func requirePackageInputs(appDir string, manifest *Manifest) error {
	for _, path := range []string{manifest.Run.Command, manifest.Web.Dist} {
		clean := filepath.Clean(path)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("invalid package path %q", path)
		}
		if _, err := os.Stat(filepath.Join(appDir, clean)); err != nil {
			return err
		}
	}
	return nil
}

func addFile(tw *tar.Writer, srcPath, archivePath string) error {
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(archivePath)
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	file, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(tw, file)
	return err
}
