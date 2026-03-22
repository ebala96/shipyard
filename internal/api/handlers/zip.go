package handlers

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractZip extracts a zip archive to a destination directory.
// It prevents zip-slip attacks by validating all file paths.
func extractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("cannot open zip: %w", err)
	}
	defer r.Close()

	if err := os.MkdirAll(dest, 0755); err != nil {
		return fmt.Errorf("cannot create destination directory: %w", err)
	}

	for _, f := range r.File {
		if err := extractZipFile(f, dest); err != nil {
			return err
		}
	}

	return nil
}

// extractZipFile extracts a single file from a zip archive.
func extractZipFile(f *zip.File, dest string) error {
	// Sanitise the path to prevent zip-slip.
	cleanPath := filepath.Clean(f.Name)
	if strings.HasPrefix(cleanPath, "..") {
		return fmt.Errorf("zip contains unsafe path %q", f.Name)
	}

	targetPath := filepath.Join(dest, cleanPath)

	if f.FileInfo().IsDir() {
		return os.MkdirAll(targetPath, 0755)
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("cannot create directory for %q: %w", f.Name, err)
	}

	outFile, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return fmt.Errorf("cannot create file %q: %w", targetPath, err)
	}
	defer outFile.Close()

	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("cannot open zip entry %q: %w", f.Name, err)
	}
	defer rc.Close()

	if _, err := io.Copy(outFile, rc); err != nil {
		return fmt.Errorf("cannot write file %q: %w", targetPath, err)
	}

	return nil
}
