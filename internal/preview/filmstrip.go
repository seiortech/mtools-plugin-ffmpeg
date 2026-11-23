package preview

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrFrameGrabFailed = errors.New("ffmpeg frame capture failed")

// BuildFilmstrip extracts numTiles evenly spaced keyframes in parallel (up to 2 workers)
// and stitches them into a horizontal JPEG at outPath.
func BuildFilmstrip(
	ctx context.Context,
	ffmpegBin, filePath string,
	durationSec float64,
	numTiles, tileW, tileH int,
	outPath string,
) error {
	if strings.TrimSpace(ffmpegBin) == "" {
		return errors.New("ffmpeg executable is missing")
	}
	if durationSec <= 0 {
		return ErrFrameGrabFailed
	}
	if numTiles <= 0 {
		numTiles = 8
	}
	if tileW <= 0 {
		tileW = 80
	}
	if tileH <= 0 {
		tileH = 45
	}

	tmpDir, err := os.MkdirTemp("", "mtools-ffstrip-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	strip := image.NewRGBA(image.Rect(0, 0, numTiles*tileW, tileH))
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2", tileW, tileH, tileW, tileH)

	tiles := make([]image.Image, numTiles)
	jobs := make(chan int, numTiles)
	for i := 0; i < numTiles; i++ {
		jobs <- i
	}
	close(jobs)

	workers := min(2, numTiles)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				sec := (float64(i) + 0.5) * durationSec / float64(numTiles)
				tilePath := filepath.Join(tmpDir, fmt.Sprintf("tile_%02d.jpg", i))
				runExtract := func(keyframeOnly bool) error {
					tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
					defer cancel()
					args := []string{"-nostdin", "-y"}
					if keyframeOnly {
						args = append(args, "-skip_frame", "nokey")
					}
					args = append(args,
						"-ss", fmt.Sprintf("%.3f", sec),
						"-i", filePath,
						"-an", "-sn", "-dn", "-threads", "1",
						"-frames:v", "1",
						"-vf", vf,
						"-q:v", "5",
						tilePath,
					)
					return exec.CommandContext(tctx, ffmpegBin, args...).Run()
				}
				if err := runExtract(true); err != nil {
					if ctx.Err() != nil {
						return
					}
					if rerr := runExtract(false); rerr != nil {
						continue
					}
				}
				f, ferr := os.Open(tilePath)
				if ferr != nil {
					continue
				}
				img, derr := jpeg.Decode(f)
				_ = f.Close()
				if derr != nil {
					continue
				}
				tiles[i] = img
			}
		}()
	}
	wg.Wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	for i, img := range tiles {
		if img == nil {
			continue
		}
		dstRect := image.Rect(i*tileW, 0, (i+1)*tileW, tileH)
		draw.Draw(strip, dstRect, img, img.Bounds().Min, draw.Src)
	}

	return writeJPEGAtomic(outPath, strip, 82)
}

// GrabFrame captures a single scaled JPEG preview frame at sec into outPath.
func GrabFrame(ctx context.Context, ffmpegBin, filePath string, sec float64, width int, outPath string) error {
	if strings.TrimSpace(ffmpegBin) == "" {
		return errors.New("ffmpeg executable is missing")
	}
	if sec < 0 {
		sec = 0
	}
	if width <= 0 {
		width = 320
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tmp := outPath + ".tmp.jpg"
	args := []string{
		"-nostdin", "-y",
		"-ss", fmt.Sprintf("%.3f", sec),
		"-i", filePath,
		"-an", "-sn", "-dn",
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:-2", width),
		"-q:v", "4",
		tmp,
	}
	cmd := exec.CommandContext(tctx, ffmpegBin, args...)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: %v", ErrFrameGrabFailed, err)
	}
	return os.Rename(tmp, outPath)
}

// SaveHDFrame extracts a full-resolution PNG/JPG frame at sec into outPath.
func SaveHDFrame(ctx context.Context, ffmpegBin, filePath string, sec float64, outPath string) error {
	if strings.TrimSpace(ffmpegBin) == "" {
		return errors.New("ffmpeg executable is missing")
	}
	if sec < 0 {
		sec = 0
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	args := []string{
		"-nostdin", "-y",
		"-ss", fmt.Sprintf("%.3f", sec),
		"-i", filePath,
		"-frames:v", "1",
		"-q:v", "2",
		outPath,
	}
	cmd := exec.CommandContext(tctx, ffmpegBin, args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrFrameGrabFailed, err)
	}
	return nil
}

// CleanupAssets removes temporary preview assets matching prefix inside assetsDir.
func CleanupAssets(assetsDir, prefix string) {
	if strings.TrimSpace(assetsDir) == "" {
		return
	}
	entries, err := os.ReadDir(assetsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if prefix == "" || strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(assetsDir, e.Name()))
		}
	}
}

func writeJPEGAtomic(outPath string, img image.Image, quality int) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	tmp := outPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	encErr := jpeg.Encode(f, img, &jpeg.Options{Quality: quality})
	closeErr := f.Close()
	if encErr != nil {
		_ = os.Remove(tmp)
		return encErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, outPath)
}
