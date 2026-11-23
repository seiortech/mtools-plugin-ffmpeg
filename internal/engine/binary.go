package engine

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/seiortech/mtools-sdk/pluginapi"
)

const ffbinariesBaseURL = "https://github.com/ffbinaries/ffbinaries-prebuilt/releases/download/v6.1/"

var (
	ErrMissingFFmpeg       = errors.New("ffmpeg executable is missing")
	ErrMissingFFprobe      = errors.New("ffprobe executable is missing")
	ErrUnsupportedPlatform = errors.New("unsupported platform for automatic binary download")
)

var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&http.Transport{}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// ResolveFFmpeg resolves ffmpeg via settings, shared tools dir, plugin data dir, or system PATH.
func ResolveFFmpeg(customPath, dataDir string, settings map[string]string) (string, error) {
	return resolveBinary(customPath, dataDir, "ffmpeg", settings, ErrMissingFFmpeg)
}

// ResolveFFprobe resolves ffprobe via settings, shared tools dir, plugin data dir, or system PATH.
func ResolveFFprobe(customPath, dataDir string, settings map[string]string) (string, error) {
	return resolveBinary(customPath, dataDir, "ffprobe", settings, ErrMissingFFprobe)
}

func resolveBinary(customPath, dataDir, baseName string, settings map[string]string, missingErr error) (string, error) {
	if resolved, ok := pluginapi.NewSharedTools(settings).Resolve(customPath, pluginapi.SharedToolSpec{
		FileName:     baseName,
		IsExecutable: true,
		CheckPATH:    true,
	}); ok {
		return resolved, nil
	}
	if p := strings.TrimSpace(customPath); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	binName := baseName
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	if strings.TrimSpace(dataDir) != "" {
		localBin := filepath.Join(dataDir, "bin", binName)
		if info, err := os.Stat(localBin); err == nil && !info.IsDir() {
			return localBin, nil
		}
	}
	if p, err := exec.LookPath(binName); err == nil {
		return p, nil
	}
	return "", missingErr
}

// FFmpegAssetURL returns the standalone ffbinaries archive URL for ffmpeg on the current OS/arch.
func FFmpegAssetURL() (string, error) {
	return ffbinaryURL("ffmpeg")
}

// FFprobeAssetURL returns the standalone ffbinaries archive URL for ffprobe on the current OS/arch.
func FFprobeAssetURL() (string, error) {
	return ffbinaryURL("ffprobe")
}

func ffbinaryURL(tool string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return ffbinariesBaseURL + tool + "-6.1-macos-64.zip", nil
	case "linux":
		if runtime.GOARCH == "arm64" {
			return ffbinariesBaseURL + tool + "-6.1-linux-arm64.zip", nil
		}
		return ffbinariesBaseURL + tool + "-6.1-linux-64.zip", nil
	case "windows":
		return ffbinariesBaseURL + tool + "-6.1-win-64.zip", nil
	default:
		return "", ErrUnsupportedPlatform
	}
}

// EnsureFFmpegInDir downloads and extracts ffmpeg directly into targetDir using HTTP Range resume.
func EnsureFFmpegInDir(ctx context.Context, targetDir string, onProgress func(done, total int64)) (string, error) {
	return ensureToolInDir(ctx, "ffmpeg", targetDir, onProgress)
}

// EnsureFFprobeInDir downloads and extracts ffprobe directly into targetDir using HTTP Range resume.
func EnsureFFprobeInDir(ctx context.Context, targetDir string, onProgress func(done, total int64)) (string, error) {
	return ensureToolInDir(ctx, "ffprobe", targetDir, onProgress)
}

func ensureToolInDir(ctx context.Context, tool, targetDir string, onProgress func(done, total int64)) (string, error) {
	assetURL, err := ffbinaryURL(tool)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	zipDest := filepath.Join(targetDir, tool+".zip")
	if err := downloadFileResumable(ctx, httpClient, assetURL, zipDest, 0o644, onProgress); err != nil {
		return "", err
	}
	defer os.Remove(zipDest)

	binName := tool
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	dest := filepath.Join(targetDir, binName)
	if err := extractSingleBinaryFromZip(zipDest, binName, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func downloadFileResumable(ctx context.Context, client *http.Client, rawURL, dest string, mode os.FileMode, onProgress func(done, total int64)) error {
	if client == nil {
		client = httpClient
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	partPath := dest + ".part"

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			_ = os.Remove(partPath)
			return ctx.Err()
		}
		var offset int64
		if info, err := os.Stat(partPath); err == nil {
			offset = info.Size()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusOK && offset > 0 {
			offset = 0
		} else if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		flags := os.O_CREATE | os.O_WRONLY
		if offset > 0 && resp.StatusCode == http.StatusPartialContent {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
			offset = 0
		}

		f, err := os.OpenFile(partPath, flags, mode)
		if err != nil {
			resp.Body.Close()
			return err
		}

		total := resp.ContentLength
		if total > 0 && offset > 0 {
			total += offset
		}
		written := offset
		buf := make([]byte, 128*1024)
		var lastEmit time.Time
		copyErr := func() error {
			defer f.Close()
			defer resp.Body.Close()
			for {
				n, rerr := resp.Body.Read(buf)
				if n > 0 {
					if _, werr := f.Write(buf[:n]); werr != nil {
						return werr
					}
					written += int64(n)
					now := time.Now()
					if onProgress != nil && (lastEmit.IsZero() || now.Sub(lastEmit) >= 100*time.Millisecond) {
						lastEmit = now
						onProgress(written, total)
					}
				}
				if rerr == io.EOF {
					return nil
				}
				if rerr != nil {
					return rerr
				}
			}
		}()

		if copyErr == nil {
			if onProgress != nil {
				onProgress(written, total)
			}
			if err := os.Chmod(partPath, mode); err != nil {
				return err
			}
			return os.Rename(partPath, dest)
		}
		lastErr = copyErr
		if ctx.Err() != nil {
			_ = os.Remove(partPath)
			return ctx.Err()
		}
	}
	return lastErr
}

func extractSingleBinaryFromZip(zipPath, targetName, destPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		if filepath.Base(f.Name) != targetName || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		tmp := destPath + ".tmp"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(rc, 256*1024*1024))
		out.Close()
		rc.Close()
		if copyErr != nil {
			_ = os.Remove(tmp)
			return copyErr
		}
		return os.Rename(tmp, destPath)
	}
	return fmt.Errorf("%s not found in downloaded archive", targetName)
}
