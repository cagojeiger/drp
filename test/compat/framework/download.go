package framework

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// DefaultFrpVersion is the frp release version used for compat testing.
const DefaultFrpVersion = "0.68.0"

// DownloadFrp downloads frps and frpc binaries from GitHub releases and
// caches them. Cache directory: $DRP_FRP_CACHE or os.TempDir()/drp-frp-cache/.
// Skips download if cached binaries exist and are executable.
// Returns (frpsPath, frpcPath, error).
func DownloadFrp(version string) (string, string, error) {
	cacheDir := os.Getenv("DRP_FRP_CACHE")
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "drp-frp-cache")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create cache dir: %w", err)
	}

	osName, archName := detectPlatform()
	prefix := fmt.Sprintf("frp_%s_%s_%s", version, osName, archName)
	frpsPath := filepath.Join(cacheDir, prefix, "frps")
	frpcPath := filepath.Join(cacheDir, prefix, "frpc")

	// Skip download if both binaries exist and are executable.
	if isExecutable(frpsPath) && isExecutable(frpcPath) {
		return frpsPath, frpcPath, nil
	}

	url := fmt.Sprintf(
		"https://github.com/fatedier/frp/releases/download/v%s/%s.tar.gz",
		version, prefix,
	)

	if err := downloadAndExtract(url, cacheDir, prefix); err != nil {
		return "", "", err
	}

	if err := os.Chmod(frpsPath, 0o755); err != nil {
		return "", "", fmt.Errorf("chmod frps: %w", err)
	}
	if err := os.Chmod(frpcPath, 0o755); err != nil {
		return "", "", fmt.Errorf("chmod frpc: %w", err)
	}

	return frpsPath, frpcPath, nil
}

func detectPlatform() (string, string) {
	osName := runtime.GOOS   // darwin, linux
	archName := runtime.GOARCH // amd64, arm64
	return osName, archName
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&0o111 != 0
}

func downloadAndExtract(url, destDir, prefix string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}

		// Only extract frps and frpc binaries from the archive.
		name := filepath.Base(hdr.Name)
		if name != "frps" && name != "frpc" {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		outDir := filepath.Join(destDir, prefix)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", outDir, err)
		}
		outPath := filepath.Join(outDir, name)
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("create %s: %w", outPath, err)
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return fmt.Errorf("extract %s: %w", name, err)
		}
		f.Close()
	}

	return nil
}
