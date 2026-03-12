package updater

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractFromZip finds targetName in zipPath and extracts it to outDir.
// Returns the path to the extracted file.
func extractFromZip(zipPath, targetName, outDir string) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	for _, f := range r.File {
		if strings.EqualFold(filepath.Base(f.Name), targetName) {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()

			outPath := filepath.Join(outDir, targetName)
			out, err := os.Create(outPath)
			if err != nil {
				return "", err
			}
			defer out.Close()

			if _, err := io.Copy(out, rc); err != nil {
				return "", err
			}
			return outPath, nil
		}
	}

	return "", fmt.Errorf("%q not found in zip", targetName)
}
