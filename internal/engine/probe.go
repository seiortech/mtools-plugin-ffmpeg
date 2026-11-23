package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrInvalidInputPath = errors.New("select an existing readable media file")
	ErrProbeFailed      = errors.New("failed to inspect media file streams")
)

type VideoStreamInfo struct {
	Index       int
	Codec       string
	Profile     string
	PixFmt      string
	Width       int
	Height      int
	FPS         float64
	BitrateKbps int
}

type AudioStreamInfo struct {
	Index         int
	Codec         string
	SampleRate    int
	Channels      int
	ChannelLayout string
	Language      string
	Title         string
	BitrateKbps   int
}

type SubStreamInfo struct {
	Index    int
	Codec    string
	Language string
	Title    string
}

type ChapterInfo struct {
	ID       int
	Title    string
	StartSec float64
	EndSec   float64
}

type MediaProbe struct {
	Path         string
	FormatName   string
	DurationSec  float64
	SizeBytes    int64
	BitrateKbps  int
	VideoStreams []VideoStreamInfo
	AudioStreams []AudioStreamInfo
	SubStreams   []SubStreamInfo
	Chapters     []ChapterInfo
}

func (p *MediaProbe) HasVideo() bool {
	return p != nil && len(p.VideoStreams) > 0
}

func (p *MediaProbe) HasAudio() bool {
	return p != nil && len(p.AudioStreams) > 0
}

// ValidateInputFile checks that path is non-empty, safe from flag injection, and an existing regular file.
func ValidateInputFile(path string) (string, int64, error) {
	clean := strings.TrimSpace(path)
	if clean == "" || strings.HasPrefix(clean, "-") {
		return "", 0, ErrInvalidInputPath
	}
	clean = filepath.Clean(clean)
	info, err := os.Stat(clean)
	if err != nil || info.IsDir() {
		return "", 0, ErrInvalidInputPath
	}
	return clean, info.Size(), nil
}

// ProbeMedia inspects a media file using ffprobe JSON output, falling back to ffmpeg -i stderr parsing.
func ProbeMedia(ctx context.Context, ffprobeBin, ffmpegBin, filePath string) (*MediaProbe, error) {
	cleanPath, sizeBytes, err := ValidateInputFile(filePath)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(ffprobeBin) != "" {
		args := []string{
			"-v", "quiet",
			"-print_format", "json",
			"-show_format",
			"-show_streams",
			"-show_chapters",
			"--", cleanPath,
		}
		cmd := exec.CommandContext(ctx, ffprobeBin, args...)
		prepareCommand(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil && stdout.Len() > 0 {
			if probe, perr := ParseFFprobeJSON(stdout.Bytes(), cleanPath, sizeBytes); perr == nil {
				return probe, nil
			}
		}
	}

	if strings.TrimSpace(ffmpegBin) == "" {
		return nil, ErrMissingFFmpeg
	}

	cmd := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-nostdin", "-i", cleanPath)
	prepareCommand(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run() // ffmpeg -i without output exits 1 while printing stream info to stderr

	probe, err := ParseFFmpegStderrProbe(stderr.String(), cleanPath, sizeBytes)
	if err != nil {
		return nil, err
	}
	return probe, nil
}

// ParseFFprobeJSON decodes ffprobe -show_format -show_streams -show_chapters JSON output.
func ParseFFprobeJSON(data []byte, filePath string, sizeBytes int64) (*MediaProbe, error) {
	var raw struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			Size       string `json:"size"`
			BitRate    string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			Index         int               `json:"index"`
			CodecType     string            `json:"codec_type"`
			CodecName     string            `json:"codec_name"`
			Profile       string            `json:"profile"`
			PixFmt        string            `json:"pix_fmt"`
			Width         int               `json:"width"`
			Height        int               `json:"height"`
			RFrameRate    string            `json:"r_frame_rate"`
			AvgFrameRate  string            `json:"avg_frame_rate"`
			SampleRate    string            `json:"sample_rate"`
			Channels      int               `json:"channels"`
			ChannelLayout string            `json:"channel_layout"`
			BitRate       string            `json:"bit_rate"`
			Tags          map[string]string `json:"tags"`
		} `json:"streams"`
		Chapters []struct {
			ID        int               `json:"id"`
			StartTime string            `json:"start_time"`
			EndTime   string            `json:"end_time"`
			Tags      map[string]string `json:"tags"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbeFailed, err)
	}

	dur, _ := strconv.ParseFloat(raw.Format.Duration, 64)
	if sz, err := strconv.ParseInt(raw.Format.Size, 10, 64); err == nil && sz > 0 {
		sizeBytes = sz
	}
	br, _ := strconv.ParseInt(raw.Format.BitRate, 10, 64)

	probe := &MediaProbe{
		Path:        filePath,
		FormatName:  raw.Format.FormatName,
		DurationSec: dur,
		SizeBytes:   sizeBytes,
		BitrateKbps: int(br / 1000),
	}

	for _, st := range raw.Streams {
		lang := tagLookup(st.Tags, "language")
		title := tagLookup(st.Tags, "title")
		stBr, _ := strconv.ParseInt(st.BitRate, 10, 64)

		switch st.CodecType {
		case "video":
			fps := parseFraction(st.AvgFrameRate)
			if fps <= 0 {
				fps = parseFraction(st.RFrameRate)
			}
			probe.VideoStreams = append(probe.VideoStreams, VideoStreamInfo{
				Index:       st.Index,
				Codec:       st.CodecName,
				Profile:     st.Profile,
				PixFmt:      st.PixFmt,
				Width:       st.Width,
				Height:      st.Height,
				FPS:         fps,
				BitrateKbps: int(stBr / 1000),
			})
		case "audio":
			sr, _ := strconv.Atoi(st.SampleRate)
			probe.AudioStreams = append(probe.AudioStreams, AudioStreamInfo{
				Index:         st.Index,
				Codec:         st.CodecName,
				SampleRate:    sr,
				Channels:      st.Channels,
				ChannelLayout: st.ChannelLayout,
				Language:      lang,
				Title:         title,
				BitrateKbps:   int(stBr / 1000),
			})
		case "subtitle":
			probe.SubStreams = append(probe.SubStreams, SubStreamInfo{
				Index:    st.Index,
				Codec:    st.CodecName,
				Language: lang,
				Title:    title,
			})
		}
	}

	for i, ch := range raw.Chapters {
		st, _ := strconv.ParseFloat(ch.StartTime, 64)
		en, _ := strconv.ParseFloat(ch.EndTime, 64)
		title := tagLookup(ch.Tags, "title")
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		probe.Chapters = append(probe.Chapters, ChapterInfo{
			ID:       ch.ID,
			Title:    title,
			StartSec: st,
			EndSec:   en,
		})
	}

	if len(probe.VideoStreams) == 0 && len(probe.AudioStreams) == 0 && len(probe.SubStreams) == 0 {
		return nil, ErrProbeFailed
	}
	return probe, nil
}

var (
	reDuration   = regexp.MustCompile(`Duration:\s*(\d+):(\d+):(\d+(?:\.\d+)?)`)
	reBitrate    = regexp.MustCompile(`bitrate:\s*(\d+)\s*kb/s`)
	reStreamLine = regexp.MustCompile(`Stream\s+#\d+:(\d+)(?:\(([^)]+)\))?[^:]*:\s*(Video|Audio|Subtitle):\s*(.+)`)
	reDims       = regexp.MustCompile(`\b(\d{2,5})x(\d{2,5})\b`)
	reFPS        = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*fps\b`)
	reSampleRate = regexp.MustCompile(`\b(\d{4,6})\s*Hz\b`)
	reChapter    = regexp.MustCompile(`Chapter\s+#\d+:(\d+):\s*start\s*(\d+(?:\.\d+)?),\s*end\s*(\d+(?:\.\d+)?)`)
)

// ParseFFmpegStderrProbe parses stream, duration, and chapter info from `ffmpeg -i` stderr when ffprobe is unavailable.
func ParseFFmpegStderrProbe(stderrText, filePath string, sizeBytes int64) (*MediaProbe, error) {
	probe := &MediaProbe{
		Path:       filePath,
		FormatName: strings.TrimPrefix(strings.ToLower(filepath.Ext(filePath)), "."),
		SizeBytes:  sizeBytes,
	}

	if m := reDuration.FindStringSubmatch(stderrText); len(m) == 4 {
		h, _ := strconv.ParseFloat(m[1], 64)
		min, _ := strconv.ParseFloat(m[2], 64)
		sec, _ := strconv.ParseFloat(m[3], 64)
		probe.DurationSec = h*3600 + min*60 + sec
	}
	if m := reBitrate.FindStringSubmatch(stderrText); len(m) == 2 {
		probe.BitrateKbps, _ = strconv.Atoi(m[1])
	}

	sc := bufio.NewScanner(strings.NewReader(stderrText))
	for sc.Scan() {
		line := sc.Text()
		if m := reStreamLine.FindStringSubmatch(line); len(m) == 5 {
			idx, _ := strconv.Atoi(m[1])
			lang := strings.TrimSpace(m[2])
			kind := m[3]
			rest := m[4]
			codec := strings.TrimSpace(strings.Split(rest, ",")[0])
			if sp := strings.IndexByte(codec, ' '); sp > 0 {
				codec = codec[:sp]
			}
			switch kind {
			case "Video":
				var w, h int
				if dm := reDims.FindStringSubmatch(rest); len(dm) == 3 {
					w, _ = strconv.Atoi(dm[1])
					h, _ = strconv.Atoi(dm[2])
				}
				var fps float64
				if fm := reFPS.FindStringSubmatch(rest); len(fm) == 2 {
					fps, _ = strconv.ParseFloat(fm[1], 64)
				}
				probe.VideoStreams = append(probe.VideoStreams, VideoStreamInfo{
					Index:  idx,
					Codec:  codec,
					Width:  w,
					Height: h,
					FPS:    fps,
				})
			case "Audio":
				var sr int
				if sm := reSampleRate.FindStringSubmatch(rest); len(sm) == 2 {
					sr, _ = strconv.Atoi(sm[1])
				}
				ch := 2
				if strings.Contains(rest, "mono") {
					ch = 1
				} else if strings.Contains(rest, "5.1") {
					ch = 6
				}
				probe.AudioStreams = append(probe.AudioStreams, AudioStreamInfo{
					Index:      idx,
					Codec:      codec,
					SampleRate: sr,
					Channels:   ch,
					Language:   lang,
				})
			case "Subtitle":
				probe.SubStreams = append(probe.SubStreams, SubStreamInfo{
					Index:    idx,
					Codec:    codec,
					Language: lang,
				})
			}
			continue
		}

		if m := reChapter.FindStringSubmatch(line); len(m) == 4 {
			id, _ := strconv.Atoi(m[1])
			st, _ := strconv.ParseFloat(m[2], 64)
			en, _ := strconv.ParseFloat(m[3], 64)
			probe.Chapters = append(probe.Chapters, ChapterInfo{
				ID:       id,
				Title:    fmt.Sprintf("Chapter %d", len(probe.Chapters)+1),
				StartSec: st,
				EndSec:   en,
			})
		}
	}

	if len(probe.VideoStreams) == 0 && len(probe.AudioStreams) == 0 && len(probe.SubStreams) == 0 {
		return nil, ErrProbeFailed
	}
	return probe, nil
}

// EstimateOutputBytes estimates output file size and returns a codec/container compatibility label.
func EstimateOutputBytes(probe *MediaProbe, mode string, opts TranscodeOptions, clipDurSec float64) (int64, string, bool) {
	if probe == nil {
		return 0, "Ready", true
	}
	dur := clipDurSec
	if dur <= 0 {
		dur = probe.DurationSec
	}

	if mode == ModeFrames {
		switch opts.FramesOp {
		case "gif":
			return int64(dur * 450 * 1024), "Animated GIF · 2-Pass Palette", true
		case "webp":
			return int64(dur * 250 * 1024), "Animated WebP", true
		case "contact_sheet":
			return 600 * 1024, fmt.Sprintf("Contact Sheet (%dx%d)", maxInt(opts.SheetCols, 4), maxInt(opts.SheetRows, 4)), true
		default:
			return 0, "Image Sequence (" + strings.ToUpper(nonEmpty(opts.FrameFormat, "png")) + ")", true
		}
	}

	warn, ok := ValidateCodecContainer(opts.Container, opts.VideoCodec, opts.AudioCodec)
	if !ok {
		return 0, warn, false
	}

	if IsAudioOnlyContainer(opts.Container) {
		abr := opts.AudioBitrateK
		if abr <= 0 {
			abr = 192
		}
		if opts.Container == "flac" {
			abr = 900
		} else if opts.Container == "wav" {
			abr = 1411
		}
		return int64((float64(abr) * 1000 / 8) * dur), strings.ToUpper(opts.Container) + " · Audio", true
	}

	if opts.VideoCodec == "copy" || (mode == ModeTrim && opts.LosslessCut) {
		if probe.DurationSec > 0 && dur > 0 && probe.SizeBytes > 0 {
			ratio := dur / probe.DurationSec
			if ratio > 1 {
				ratio = 1
			}
			return int64(float64(probe.SizeBytes) * ratio), strings.ToUpper(opts.Container) + " · Stream Copy (Lossless)", true
		}
		return probe.SizeBytes, strings.ToUpper(opts.Container) + " · Stream Copy", true
	}

	if opts.QualityMode == "target_mb" && opts.TargetSizeMB > 0 {
		return int64(opts.TargetSizeMB * 1024 * 1024), fmt.Sprintf("%s · Target %.0f MB", strings.ToUpper(opts.Container), opts.TargetSizeMB), true
	}

	var estKbps int
	if opts.QualityMode == "bitrate" && opts.VideoBitrateK > 0 {
		estKbps = opts.VideoBitrateK
	} else {
		crf := opts.CRF
		if crf <= 0 {
			crf = 23
		}
		base := 3500
		if opts.VideoCodec == "libx265" || opts.VideoCodec == "libsvtav1" {
			base = 2100
		}
		// Scale roughly with CRF relative to 23
		factor := 1.0 + float64(23-crf)*0.12
		if factor < 0.25 {
			factor = 0.25
		}
		estKbps = int(float64(base) * factor)
	}
	if !opts.MuteAudio {
		abr := opts.AudioBitrateK
		if abr <= 0 {
			abr = 192
		}
		estKbps += abr
	}

	estBytes := int64((float64(estKbps) * 1000 / 8) * dur)
	return estBytes, strings.ToUpper(opts.Container) + " · " + friendlyCodecLabel(opts.VideoCodec), true
}

func friendlyCodecLabel(vc string) string {
	switch vc {
	case "libx264":
		return "H.264"
	case "libx265":
		return "H.265 / HEVC"
	case "libvpx-vp9":
		return "VP9"
	case "libsvtav1":
		return "AV1"
	case "prores_ks":
		return "ProRes"
	case "copy":
		return "Stream Copy"
	default:
		return vc
	}
}

func tagLookup(tags map[string]string, key string) string {
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parseFraction(raw string) float64 {
	parts := strings.Split(strings.TrimSpace(raw), "/")
	if len(parts) == 1 {
		v, _ := strconv.ParseFloat(parts[0], 64)
		return v
	}
	if len(parts) == 2 {
		num, err1 := strconv.ParseFloat(parts[0], 64)
		den, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 == nil && err2 == nil && den > 0 {
			return num / den
		}
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
