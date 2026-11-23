package preview

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"
)

var ErrFrameGrabFailed = errors.New("ffmpeg frame capture failed")

// BuildFilmstripFilter returns an FFmpeg video filter that scales and center-crops
// each frame to fill tileW x tileH edge-to-edge without letterbox or pillarbox padding.
func BuildFilmstripFilter(tileW, tileH int) string {
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase:flags=fast_bilinear,crop=%d:%d,setsar=1", tileW, tileH, tileW, tileH)
}

// BuildSingleProcessFilmstripArgs builds a single-invocation FFmpeg command that seeks
// to numTiles evenly spaced timestamps with -noaccurate_seek and stacks them with hstack.
func BuildSingleProcessFilmstripArgs(
	filePath string,
	durationSec float64,
	numTiles, tileW, tileH int,
	outPath string,
) []string {
	return BuildSingleProcessFilmstripArgsWithHW(filePath, durationSec, numTiles, tileW, tileH, engine.HWPlan{}, outPath)
}

// BuildSingleProcessFilmstripArgsWithHW builds a single-invocation FFmpeg filmstrip command
// with optional per-input hardware decoding.
func BuildSingleProcessFilmstripArgsWithHW(
	filePath string,
	durationSec float64,
	numTiles, tileW, tileH int,
	hw engine.HWPlan,
	outPath string,
) []string {
	if numTiles <= 0 {
		numTiles = 8
	}
	if tileW <= 0 {
		tileW = 80
	}
	if tileH <= 0 {
		tileH = 45
	}
	maxSec := durationSec - 0.05
	if maxSec < 0 {
		maxSec = 0
	}

	hwPrefix := engine.DecodeInputPrefix(hw)
	args := make([]string, 0, 12+numTiles*(8+len(hwPrefix)))
	args = append(args, "-nostdin", "-y", "-hide_banner", "-loglevel", "error")

	vf := BuildFilmstripFilter(tileW, tileH)
	filterParts := make([]string, 0, numTiles+1)
	stackInputs := make([]string, 0, numTiles)

	for i := 0; i < numTiles; i++ {
		sec := (float64(i) + 0.5) * durationSec / float64(numTiles)
		if sec > maxSec {
			sec = maxSec
		}
		if sec < 0 {
			sec = 0
		}
		args = append(args, hwPrefix...)
		args = append(args,
			"-noaccurate_seek",
			"-ss", fmt.Sprintf("%.3f", sec),
			"-an", "-sn", "-dn",
			"-i", filePath,
		)
		filterParts = append(filterParts, fmt.Sprintf("[%d:v:0]%s,setpts=PTS-STARTPTS[v%d]", i, vf, i))
		stackInputs = append(stackInputs, fmt.Sprintf("[v%d]", i))
	}

	outLabel := "[v0]"
	if numTiles > 1 {
		filterParts = append(filterParts, fmt.Sprintf("%shstack=inputs=%d:shortest=1[out]", strings.Join(stackInputs, ""), numTiles))
		outLabel = "[out]"
	}

	args = append(args,
		"-filter_complex", strings.Join(filterParts, ";"),
		"-map", outLabel,
		"-frames:v", "1",
		"-q:v", "6",
		"-update", "1",
		outPath,
	)
	return args
}

// BuildFilmstrip extracts numTiles evenly spaced frames into a horizontal JPEG at outPath,
// preferring a single-process FFmpeg invocation and falling back to parallel per-tile grabs.
func BuildFilmstrip(
	ctx context.Context,
	ffmpegBin, filePath string,
	durationSec float64,
	numTiles, tileW, tileH int,
	outPath string,
) error {
	return BuildFilmstripWithHW(ctx, ffmpegBin, filePath, durationSec, numTiles, tileW, tileH, engine.HWPlan{}, outPath)
}

// BuildFilmstripWithHW extracts numTiles evenly spaced frames into a horizontal JPEG at outPath,
// attempting hardware-accelerated single-process decoding first when configured, then falling back to CPU.
func BuildFilmstripWithHW(
	ctx context.Context,
	ffmpegBin, filePath string,
	durationSec float64,
	numTiles, tileW, tileH int,
	hw engine.HWPlan,
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
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	tmp := outPath + ".tmp.jpg"
	if len(engine.DecodeInputPrefix(hw)) > 0 {
		hctx, hcancel := context.WithTimeout(ctx, 15*time.Second)
		hwArgs := BuildSingleProcessFilmstripArgsWithHW(filePath, durationSec, numTiles, tileW, tileH, hw, tmp)
		err := exec.CommandContext(hctx, ffmpegBin, hwArgs...).Run()
		hcancel()
		if err == nil {
			if info, serr := os.Stat(tmp); serr == nil && info.Size() > 0 {
				if rerr := os.Rename(tmp, outPath); rerr == nil {
					return nil
				}
			}
		}
		_ = os.Remove(tmp)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	singleArgs := BuildSingleProcessFilmstripArgs(filePath, durationSec, numTiles, tileW, tileH, tmp)
	err := exec.CommandContext(sctx, ffmpegBin, singleArgs...).Run()
	cancel()
	if err == nil {
		if info, serr := os.Stat(tmp); serr == nil && info.Size() > 0 {
			if rerr := os.Rename(tmp, outPath); rerr == nil {
				return nil
			}
		}
	}
	_ = os.Remove(tmp)
	if ctx.Err() != nil {
		return ctx.Err()
	}

	return buildFilmstripPerTile(ctx, ffmpegBin, filePath, durationSec, numTiles, tileW, tileH, outPath)
}

func buildFilmstripPerTile(
	ctx context.Context,
	ffmpegBin, filePath string,
	durationSec float64,
	numTiles, tileW, tileH int,
	outPath string,
) error {
	tmpDir, err := os.MkdirTemp("", "mtools-ffstrip-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	vf := BuildFilmstripFilter(tileW, tileH)
	maxSec := durationSec - 0.05
	if maxSec < 0 {
		maxSec = 0
	}

	tiles := make([]image.Image, numTiles)
	jobs := make(chan int, numTiles)
	for i := 0; i < numTiles; i++ {
		jobs <- i
	}
	close(jobs)

	workers := min(4, numTiles)
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
				if sec > maxSec {
					sec = maxSec
				}
				if sec < 0 {
					sec = 0
				}
				tilePath := filepath.Join(tmpDir, fmt.Sprintf("tile_%02d.jpg", i))
				tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
				args := []string{
					"-nostdin", "-y", "-hide_banner", "-loglevel", "error",
					"-noaccurate_seek",
					"-ss", fmt.Sprintf("%.3f", sec),
					"-i", filePath,
					"-an", "-sn", "-dn", "-threads", "1",
					"-frames:v", "1",
					"-vf", vf,
					"-q:v", "6",
					tilePath,
				}
				err := exec.CommandContext(tctx, ffmpegBin, args...).Run()
				cancel()
				if err != nil {
					continue
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
	strip, ok := assembleFilmstrip(tiles, tileW, tileH)
	if !ok {
		return ErrFrameGrabFailed
	}

	return writeJPEGAtomic(outPath, strip, 82)
}

func assembleFilmstrip(tiles []image.Image, tileW, tileH int) (*image.RGBA, bool) {
	numTiles := len(tiles)
	if numTiles == 0 || tileW <= 0 || tileH <= 0 {
		return nil, false
	}
	hasAny := false
	for _, img := range tiles {
		if img != nil {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return nil, false
	}

	strip := image.NewRGBA(image.Rect(0, 0, numTiles*tileW, tileH))
	baseCol := color.RGBA{R: 31, G: 40, B: 34, A: 255}
	draw.Draw(strip, strip.Bounds(), &image.Uniform{C: baseCol}, image.Point{}, draw.Src)

	for i := range tiles {
		img := tiles[i]
		if img == nil {
			for d := 1; d < numTiles; d++ {
				if i-d >= 0 && tiles[i-d] != nil {
					img = tiles[i-d]
					break
				}
				if i+d < numTiles && tiles[i+d] != nil {
					img = tiles[i+d]
					break
				}
			}
		}
		if img == nil {
			continue
		}
		dstRect := image.Rect(i*tileW, 0, (i+1)*tileW, tileH)
		draw.Draw(strip, dstRect, img, img.Bounds().Min, draw.Src)
	}
	return strip, true
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
