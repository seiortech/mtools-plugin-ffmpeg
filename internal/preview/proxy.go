package preview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"
)

// NeedsPreviewProxy reports whether the probed media file uses a container or codec
// that the host OS media framework cannot play directly and benefits from a fast MP4 proxy.
func NeedsPreviewProxy(probe *engine.MediaProbe) bool {
	if probe == nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(probe.Path))
	switch ext {
	case ".mkv", ".webm", ".avi", ".flv", ".ts", ".m2ts", ".mts", ".vob", ".ogv", ".ogg", ".opus", ".flac", ".wmv":
		return true
	}

	if probe.HasVideo() {
		vc := strings.ToLower(probe.VideoStreams[0].Codec)
		switch vc {
		case "h264", "avc", "avc1":
			return false
		case "hevc", "h265", "hev1", "hvc1":
			return runtime.GOOS == "linux"
		case "prores", "prores_ks":
			return runtime.GOOS != "darwin"
		default:
			return true
		}
	}
	return false
}

// BuildProxyArgs returns deterministic FFmpeg arguments to create a playable MP4/M4A proxy.
func BuildProxyArgs(filePath string, probe *engine.MediaProbe, outPath string) []string {
	args := []string{"-nostdin", "-y", "-hide_banner", "-i", filePath}

	if probe != nil && !probe.HasVideo() {
		return append(args, "-vn", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart", outPath)
	}

	canCopyVideo := false
	canCopyAudio := true
	if probe != nil && probe.HasVideo() {
		vc := strings.ToLower(probe.VideoStreams[0].Codec)
		if vc == "h264" || vc == "avc" || ((vc == "hevc" || vc == "h265") && runtime.GOOS != "linux") {
			canCopyVideo = true
		}
	}
	if probe != nil && probe.HasAudio() {
		ac := strings.ToLower(probe.AudioStreams[0].Codec)
		if ac != "aac" && ac != "mp3" {
			canCopyAudio = false
		}
	}

	if canCopyVideo && canCopyAudio {
		return append(args, "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-movflags", "+faststart", outPath)
	}
	if canCopyVideo && !canCopyAudio {
		return append(args, "-map", "0:v:0", "-map", "0:a:0?", "-c:v", "copy", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", outPath)
	}

	return append(args,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-vf", "scale=-2:min(ih\\,480)",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-crf", "30",
		"-threads", "2",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-b:a", "128k",
		"-movflags", "+faststart",
		outPath,
	)
}

// BuildHardwareProxyArgs returns macOS VideoToolbox 480p arguments when full video transcoding is needed.
func BuildHardwareProxyArgs(filePath string, probe *engine.MediaProbe, outPath string) ([]string, bool) {
	if runtime.GOOS != "darwin" || probe == nil || !probe.HasVideo() {
		return nil, false
	}
	vc := strings.ToLower(probe.VideoStreams[0].Codec)
	if vc == "h264" || vc == "avc" || vc == "hevc" || vc == "h265" {
		return nil, false
	}
	return []string{
		"-nostdin", "-y", "-hide_banner", "-i", filePath,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-vf", "scale=-2:min(ih\\,480)",
		"-c:v", "h264_videotoolbox",
		"-b:v", "1500k",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-b:a", "128k",
		"-movflags", "+faststart",
		outPath,
	}, true
}

// BuildPlayableProxy creates a temporary playable MP4 proxy atomically at outPath.
func BuildPlayableProxy(ctx context.Context, ffmpegBin, filePath string, probe *engine.MediaProbe, outPath string) error {
	if strings.TrimSpace(ffmpegBin) == "" {
		return errors.New("ffmpeg executable is missing")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	tmp := outPath + ".tmp.mp4"
	if hwArgs, ok := BuildHardwareProxyArgs(filePath, probe, tmp); ok {
		if err := exec.CommandContext(pctx, ffmpegBin, hwArgs...).Run(); err == nil {
			return os.Rename(tmp, outPath)
		}
		_ = os.Remove(tmp)
		if pctx.Err() != nil {
			return fmt.Errorf("preview proxy failed: %w", pctx.Err())
		}
	}
	args := BuildProxyArgs(filePath, probe, tmp)
	cmd := exec.CommandContext(pctx, ffmpegBin, args...)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("preview proxy failed: %w", err)
	}
	return os.Rename(tmp, outPath)
}
