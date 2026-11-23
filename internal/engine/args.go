package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	ModeConvert = "convert"
	ModeTrim    = "trim"
	ModeStreams = "streams"
	ModeFrames  = "frames"
)

var (
	ErrInvalidTimeRange          = errors.New("invalid clip time range; start must be less than end")
	ErrInvalidTargetSize         = errors.New("target file size is too small for the selected duration")
	ErrUnsupportedCodecContainer = errors.New("selected codec is incompatible with the chosen container")
	ErrNoInputFiles              = errors.New("select at least one media file first")
)

type ClipSegment struct {
	ID       string
	Title    string
	StartSec float64
	EndSec   float64
}

type TranscodeOptions struct {
	Preset string

	// Convert & Compress
	Container     string
	VideoCodec    string
	AudioCodec    string
	QualityMode   string // "crf", "target_mb", "bitrate"
	CRF           int
	TargetSizeMB  float64
	VideoBitrateK int
	AudioBitrateK int
	EncoderPreset string
	ScaleHeight   int // 0 = original, 2160, 1080, 720, 480

	// Trim & Merge
	LosslessCut bool
	ConcatMode  string // "single", "split_clips", "concat_clips", "concat_files"

	// Audio, Subtitles & Streams
	AudioTrackIdx     int // -1 = default/all
	MuteAudio         bool
	ExternalAudioPath string
	Loudnorm          bool
	VolumeDB          int
	SubMode           string // "keep", "strip", "soft_mux", "burn"
	SubTrackIdx       int
	ExternalSubPath   string
	StripMetadata     bool
	MetaTitle         string
	MetaArtist        string
	MetaComment       string

	// GIF, Frames & Contact Sheet
	FramesOp         string // "gif", "webp", "extract_frames", "contact_sheet"
	GIFFPS           int
	GIFWidth         int
	FrameIntervalSec float64
	FrameFormat      string // "png", "jpg"
	SheetCols        int
	SheetRows        int

	Overwrite bool
}

type JobSpec struct {
	ID         string
	Mode       string
	InputPaths []string
	StartSec   float64
	EndSec     float64
	Clips      []ClipSegment
	Opts       TranscodeOptions
	OutputDir  string
	HW         HWPlan
}

type CommandStep struct {
	Args                   []string
	FallbackArgs           []string
	TempOutput             string
	FinalOutput            string
	MultiOutputDir         string
	DurationSec            float64
	ConcatListContent      string
	ConcatInputPlaceholder string
}

type CommandPlan struct {
	Steps   []CommandStep
	Summary string
}

func IsAudioOnlyContainer(container string) bool {
	switch strings.ToLower(strings.TrimSpace(container)) {
	case "mp3", "m4a", "flac", "wav", "opus":
		return true
	default:
		return false
	}
}

// ValidateCodecContainer checks whether video/audio codecs are valid for the target container.
func ValidateCodecContainer(container, vcodec, acodec string) (string, bool) {
	c := strings.ToLower(strings.TrimSpace(container))
	if c == "" {
		c = "mp4"
	}
	if IsAudioOnlyContainer(c) {
		return "", true
	}
	if vcodec == "copy" || vcodec == "" {
		return "", true
	}
	switch c {
	case "webm":
		if vcodec != "libvpx-vp9" && vcodec != "libsvtav1" {
			return "WebM requires VP9 or AV1 video codec.", false
		}
	case "mp4":
		if vcodec == "prores_ks" {
			return "ProRes requires MOV or MKV container.", false
		}
	}
	return "", true
}

// BuildCommandPlan validates JobSpec and constructs deterministic FFmpeg argv slices.
func BuildCommandPlan(spec JobSpec, probe *MediaProbe) (CommandPlan, error) {
	if len(spec.InputPaths) == 0 {
		return CommandPlan{}, ErrNoInputFiles
	}
	for _, p := range spec.InputPaths {
		if strings.TrimSpace(p) == "" || strings.HasPrefix(strings.TrimSpace(p), "-") {
			return CommandPlan{}, ErrInvalidInputPath
		}
	}
	outDir := strings.TrimSpace(spec.OutputDir)
	if outDir == "" {
		outDir = filepath.Dir(spec.InputPaths[0])
	}

	mode := spec.Mode
	if mode == "" {
		mode = ModeConvert
	}

	var plan CommandPlan
	var err error
	switch mode {
	case ModeConvert:
		plan, err = buildConvertPlan(spec, probe, outDir)
	case ModeTrim:
		plan, err = buildTrimPlan(spec, probe, outDir)
	case ModeStreams:
		plan, err = buildStreamsPlan(spec, probe, outDir)
	case ModeFrames:
		plan, err = buildFramesPlan(spec, probe, outDir)
	default:
		plan, err = buildConvertPlan(spec, probe, outDir)
	}
	if err != nil {
		return CommandPlan{}, err
	}
	if spec.HW.Active() {
		cpuSpec := spec
		cpuSpec.HW = HWPlan{Mode: HWAccelOff}
		if cpuPlan, cerr := BuildCommandPlan(cpuSpec, probe); cerr == nil && len(cpuPlan.Steps) == len(plan.Steps) {
			for i := range plan.Steps {
				if !slices.Equal(plan.Steps[i].Args, cpuPlan.Steps[i].Args) {
					plan.Steps[i].FallbackArgs = cpuPlan.Steps[i].Args
				}
			}
		}
	}
	return plan, nil
}

func buildConvertPlan(spec JobSpec, probe *MediaProbe, outDir string) (CommandPlan, error) {
	opts := spec.Opts
	container := nonEmpty(strings.ToLower(opts.Container), "mp4")
	vcodec := nonEmpty(opts.VideoCodec, "libx264")
	if warn, ok := ValidateCodecContainer(container, vcodec, opts.AudioCodec); !ok {
		return CommandPlan{}, fmt.Errorf("%w: %s", ErrUnsupportedCodecContainer, warn)
	}

	inPath := spec.InputPaths[0]
	dur := effectiveDuration(spec.StartSec, spec.EndSec, probe)
	base := baseNameNoExt(inPath)
	finalOut := filepath.Join(outDir, base+"_converted."+container)
	tempOut := tempPathFor(finalOut)

	willDecode := !IsAudioOnlyContainer(container) && vcodec != "copy"
	args := baseInputArgsWithHW(inPath, spec.StartSec, spec.EndSec, probe, spec.HW, willDecode)

	if IsAudioOnlyContainer(container) {
		args = append(args, "-vn")
		args = appendAudioArgs(args, container, opts)
	} else if vcodec == "copy" {
		args = append(args, "-c", "copy")
	} else {
		effCodec, _ := MapVideoEncoder(vcodec, spec.HW)
		abr := opts.AudioBitrateK
		if abr <= 0 {
			abr = 192
		}
		var vKbps, maxRateKbps, bufSizeKbps int
		switch opts.QualityMode {
		case "target_mb":
			if opts.TargetSizeMB <= 0 || dur <= 0 {
				return CommandPlan{}, ErrInvalidTargetSize
			}
			totalKbps := int((opts.TargetSizeMB * 8192) / dur)
			vKbps = totalKbps - abr
			if opts.MuteAudio {
				vKbps = totalKbps
			}
			if vKbps < 80 {
				return CommandPlan{}, ErrInvalidTargetSize
			}
			maxRateKbps = int(float64(vKbps) * 1.25)
			bufSizeKbps = vKbps * 2
		case "bitrate":
			vKbps = opts.VideoBitrateK
			if vKbps <= 0 {
				vKbps = 2500
			}
		}
		args = AppendVideoEncoderArgs(args, effCodec, container, opts.CRF, opts.EncoderPreset, opts.ScaleHeight, vKbps, maxRateKbps, bufSizeKbps, spec.HW)

		if opts.MuteAudio {
			args = append(args, "-an")
		} else {
			args = appendAudioArgs(args, container, opts)
		}
		if container == "mp4" || container == "mov" {
			args = append(args, "-movflags", "+faststart")
		}
	}

	args = append(args, tempOut)
	return CommandPlan{
		Steps:   []CommandStep{{Args: args, TempOutput: tempOut, FinalOutput: finalOut, DurationSec: dur}},
		Summary: "Converted " + filepath.Base(finalOut),
	}, nil
}

func buildTrimPlan(spec JobSpec, probe *MediaProbe, outDir string) (CommandPlan, error) {
	opts := spec.Opts
	inPath := spec.InputPaths[0]
	container := nonEmpty(strings.ToLower(opts.Container), strings.TrimPrefix(strings.ToLower(filepath.Ext(inPath)), "."))
	if container == "" {
		container = "mp4"
	}

	if opts.ConcatMode == "concat_files" && len(spec.InputPaths) >= 2 {
		var listBuf strings.Builder
		var totalDur float64
		for _, p := range spec.InputPaths {
			listBuf.WriteString("file '")
			listBuf.WriteString(escapeConcatPath(p))
			listBuf.WriteString("'\n")
		}
		if probe != nil {
			totalDur = probe.DurationSec * float64(len(spec.InputPaths))
		}
		finalOut := filepath.Join(outDir, baseNameNoExt(inPath)+"_merged."+container)
		tempOut := tempPathFor(finalOut)
		placeholder := "__CONCAT_LIST__"
		args := []string{"-nostdin", "-y", "-hide_banner", "-progress", "pipe:1", "-f", "concat", "-safe", "0", "-i", placeholder, "-c", "copy", tempOut}
		return CommandPlan{
			Steps: []CommandStep{{
				Args:                   args,
				TempOutput:             tempOut,
				FinalOutput:            finalOut,
				DurationSec:            totalDur,
				ConcatListContent:      listBuf.String(),
				ConcatInputPlaceholder: placeholder,
			}},
			Summary: fmt.Sprintf("Merged %d files into %s", len(spec.InputPaths), filepath.Base(finalOut)),
		}, nil
	}

	segments := spec.Clips
	if len(segments) == 0 {
		if spec.EndSec <= spec.StartSec {
			return CommandPlan{}, ErrInvalidTimeRange
		}
		segments = []ClipSegment{{ID: "1", Title: "trim", StartSec: spec.StartSec, EndSec: spec.EndSec}}
	}

	var steps []CommandStep
	for i, seg := range segments {
		if seg.EndSec <= seg.StartSec {
			return CommandPlan{}, ErrInvalidTimeRange
		}
		suffix := fmt.Sprintf("_clip_%02d", i+1)
		if len(segments) == 1 {
			suffix = "_trimmed"
		}
		finalOut := filepath.Join(outDir, baseNameNoExt(inPath)+suffix+"."+container)
		tempOut := tempPathFor(finalOut)
		args := []string{"-nostdin", "-y", "-hide_banner", "-progress", "pipe:1"}
		if !opts.LosslessCut {
			args = append(args, DecodeInputPrefix(spec.HW)...)
		}
		args = append(args,
			"-ss", fmt.Sprintf("%.3f", seg.StartSec),
			"-to", fmt.Sprintf("%.3f", seg.EndSec),
			"-i", inPath,
		)
		if opts.LosslessCut {
			args = append(args, "-c", "copy")
		} else {
			vcodec := nonEmpty(opts.VideoCodec, "libx264")
			effCodec, _ := MapVideoEncoder(vcodec, spec.HW)
			args = AppendVideoEncoderArgs(args, effCodec, container, maxInt(opts.CRF, 20), opts.EncoderPreset, 0, 0, 0, 0, spec.HW)
			args = appendAudioArgs(args, container, opts)
		}
		if container == "mp4" || container == "mov" {
			args = append(args, "-movflags", "+faststart")
		}
		args = append(args, tempOut)
		steps = append(steps, CommandStep{
			Args:        args,
			TempOutput:  tempOut,
			FinalOutput: finalOut,
			DurationSec: seg.EndSec - seg.StartSec,
		})
	}

	return CommandPlan{
		Steps:   steps,
		Summary: fmt.Sprintf("Exported %d trimmed clip(s)", len(steps)),
	}, nil
}

func buildStreamsPlan(spec JobSpec, probe *MediaProbe, outDir string) (CommandPlan, error) {
	opts := spec.Opts
	inPath := spec.InputPaths[0]
	container := nonEmpty(strings.ToLower(opts.Container), strings.TrimPrefix(strings.ToLower(filepath.Ext(inPath)), "."))
	if container == "" {
		container = "mp4"
	}
	dur := effectiveDuration(spec.StartSec, spec.EndSec, probe)
	finalOut := filepath.Join(outDir, baseNameNoExt(inPath)+"_remuxed."+container)
	tempOut := tempPathFor(finalOut)

	hasExtAudio := strings.TrimSpace(opts.ExternalAudioPath) != ""
	hasExtSub := strings.TrimSpace(opts.ExternalSubPath) != ""
	willDecode := hasExtSub && opts.SubMode == "burn"
	args := baseInputArgsWithHW(inPath, spec.StartSec, spec.EndSec, probe, spec.HW, willDecode)

	if hasExtAudio {
		args = append(args, "-i", strings.TrimSpace(opts.ExternalAudioPath))
	}
	if hasExtSub && opts.SubMode == "soft_mux" {
		args = append(args, "-i", strings.TrimSpace(opts.ExternalSubPath))
	}

	// Video mapping & filters
	args = append(args, "-map", "0:v:0?")
	if hasExtSub && opts.SubMode == "burn" {
		vcodec := nonEmpty(opts.VideoCodec, "libx264")
		if vcodec == "copy" {
			vcodec = "libx264"
		}
		args = append(args, "-vf", "subtitles="+escapeFilterPath(opts.ExternalSubPath), "-c:v", vcodec, "-crf", strconv.Itoa(maxInt(opts.CRF, 21)), "-pix_fmt", "yuv420p")
	} else {
		args = append(args, "-c:v", "copy")
	}

	// Audio mapping & filters
	if opts.MuteAudio {
		args = append(args, "-an")
	} else {
		if hasExtAudio {
			args = append(args, "-map", "1:a:0", "-shortest")
		} else if opts.AudioTrackIdx >= 0 {
			args = append(args, "-map", fmt.Sprintf("0:a:%d?", opts.AudioTrackIdx))
		} else {
			args = append(args, "-map", "0:a?")
		}

		var afilters []string
		if opts.Loudnorm {
			afilters = append(afilters, "loudnorm=I=-16:TP=-1.5:LRA=11")
		}
		if opts.VolumeDB != 0 {
			afilters = append(afilters, fmt.Sprintf("volume=%ddB", opts.VolumeDB))
		}
		if len(afilters) > 0 {
			args = append(args, "-af", strings.Join(afilters, ","))
			args = appendAudioArgs(args, container, opts)
		} else if hasExtAudio {
			args = appendAudioArgs(args, container, opts)
		} else {
			args = append(args, "-c:a", "copy")
		}
	}

	// Subtitle mapping
	switch opts.SubMode {
	case "strip":
		args = append(args, "-sn")
	case "soft_mux":
		if hasExtSub {
			subInputIdx := 1
			if hasExtAudio {
				subInputIdx = 2
			}
			args = append(args, "-map", fmt.Sprintf("%d:0", subInputIdx))
			if container == "mp4" || container == "mov" {
				args = append(args, "-c:s", "mov_text")
			} else {
				args = append(args, "-c:s", "srt")
			}
		}
	case "burn":
		args = append(args, "-sn")
	default:
		args = append(args, "-map", "0:s?", "-c:s", "copy")
	}

	// Metadata
	if opts.StripMetadata {
		args = append(args, "-map_metadata", "-1")
	}
	if t := strings.TrimSpace(opts.MetaTitle); t != "" {
		args = append(args, "-metadata", "title="+t)
	}
	if a := strings.TrimSpace(opts.MetaArtist); a != "" {
		args = append(args, "-metadata", "artist="+a)
	}
	if c := strings.TrimSpace(opts.MetaComment); c != "" {
		args = append(args, "-metadata", "comment="+c)
	}

	args = append(args, tempOut)
	return CommandPlan{
		Steps:   []CommandStep{{Args: args, TempOutput: tempOut, FinalOutput: finalOut, DurationSec: dur}},
		Summary: "Updated streams & metadata in " + filepath.Base(finalOut),
	}, nil
}

func buildFramesPlan(spec JobSpec, probe *MediaProbe, outDir string) (CommandPlan, error) {
	opts := spec.Opts
	inPath := spec.InputPaths[0]
	base := baseNameNoExt(inPath)
	dur := effectiveDuration(spec.StartSec, spec.EndSec, probe)
	args := baseInputArgsWithHW(inPath, spec.StartSec, spec.EndSec, probe, spec.HW, true)

	switch opts.FramesOp {
	case "webp":
		fps := clampInt(opts.GIFFPS, 15, 5, 30)
		width := clampInt(opts.GIFWidth, 480, 160, 1280)
		finalOut := filepath.Join(outDir, base+".webp")
		tempOut := tempPathFor(finalOut)
		args = append(args, "-vf", fmt.Sprintf("fps=%d,scale=%d:-1:flags=lanczos", fps, width), "-loop", "0", "-an", tempOut)
		return CommandPlan{
			Steps:   []CommandStep{{Args: args, TempOutput: tempOut, FinalOutput: finalOut, DurationSec: dur}},
			Summary: "Exported animated WebP " + filepath.Base(finalOut),
		}, nil

	case "extract_frames":
		interval := opts.FrameIntervalSec
		if interval <= 0 {
			interval = 2
		}
		ext := nonEmpty(strings.ToLower(opts.FrameFormat), "png")
		if ext != "jpg" && ext != "png" {
			ext = "png"
		}
		framesDir := filepath.Join(outDir, base+"_frames")
		pattern := filepath.Join(framesDir, "frame_%04d."+ext)
		args = append(args, "-vf", fmt.Sprintf("fps=1/%.3f", interval), "-q:v", "2", pattern)
		return CommandPlan{
			Steps:   []CommandStep{{Args: args, TempOutput: pattern, FinalOutput: framesDir, MultiOutputDir: framesDir, DurationSec: dur}},
			Summary: "Extracted frames into " + filepath.Base(framesDir),
		}, nil

	case "contact_sheet":
		cols := clampInt(opts.SheetCols, 4, 2, 8)
		rows := clampInt(opts.SheetRows, 4, 2, 8)
		total := cols * rows
		stepDur := dur
		if stepDur <= 0 {
			stepDur = 60
		}
		fpsExpr := fmt.Sprintf("fps=%d/%.3f", total, stepDur)
		finalOut := filepath.Join(outDir, base+"_sheet.jpg")
		tempOut := tempPathFor(finalOut)
		args = append(args, "-vf", fmt.Sprintf("%s,scale=320:-1,tile=%dx%d", fpsExpr, cols, rows), "-frames:v", "1", "-q:v", "3", tempOut)
		return CommandPlan{
			Steps:   []CommandStep{{Args: args, TempOutput: tempOut, FinalOutput: finalOut, DurationSec: dur}},
			Summary: fmt.Sprintf("Generated %dx%d contact sheet %s", cols, rows, filepath.Base(finalOut)),
		}, nil

	default: // "gif"
		fps := clampInt(opts.GIFFPS, 15, 5, 30)
		width := clampInt(opts.GIFWidth, 480, 160, 1280)
		finalOut := filepath.Join(outDir, base+".gif")
		tempOut := tempPathFor(finalOut)
		vf := fmt.Sprintf("fps=%d,scale=%d:-1:flags=lanczos,split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse", fps, width)
		args = append(args, "-vf", vf, "-loop", "0", tempOut)
		return CommandPlan{
			Steps:   []CommandStep{{Args: args, TempOutput: tempOut, FinalOutput: finalOut, DurationSec: dur}},
			Summary: "Exported 2-pass GIF " + filepath.Base(finalOut),
		}, nil
	}
}

func baseInputArgsWithHW(inPath string, startSec, endSec float64, probe *MediaProbe, hw HWPlan, willDecode bool) []string {
	args := []string{"-nostdin", "-y", "-hide_banner", "-progress", "pipe:1"}
	if willDecode {
		args = append(args, DecodeInputPrefix(hw)...)
	}
	totalDur := 0.0
	if probe != nil {
		totalDur = probe.DurationSec
	}
	if startSec > 0.05 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", startSec))
	}
	if endSec > 0 && (totalDur <= 0 || endSec < totalDur-0.05) && endSec > startSec {
		args = append(args, "-to", fmt.Sprintf("%.3f", endSec))
	}
	args = append(args, "-i", inPath)
	return args
}

func appendAudioArgs(args []string, container string, opts TranscodeOptions) []string {
	abr := opts.AudioBitrateK
	if abr <= 0 {
		abr = 192
	}
	switch container {
	case "mp3":
		return append(args, "-c:a", "libmp3lame", "-b:a", fmt.Sprintf("%dk", abr))
	case "flac":
		return append(args, "-c:a", "flac")
	case "wav":
		return append(args, "-c:a", "pcm_s16le")
	case "opus", "webm":
		return append(args, "-c:a", "libopus", "-b:a", fmt.Sprintf("%dk", abr))
	default:
		ac := nonEmpty(opts.AudioCodec, "aac")
		if ac == "copy" {
			return append(args, "-c:a", "copy")
		}
		return append(args, "-c:a", ac, "-b:a", fmt.Sprintf("%dk", abr))
	}
}

// FormatCommandPreview returns a concise redacted command line preview for the UI.
func FormatCommandPreview(ffmpegBin string, plan CommandPlan) string {
	bin := filepath.Base(ffmpegBin)
	if bin == "" || bin == "." {
		bin = "ffmpeg"
	}
	if len(plan.Steps) == 0 {
		return bin
	}
	step := plan.Steps[0]
	var parts []string
	parts = append(parts, bin)
	for i, a := range step.Args {
		if a == "-nostdin" || a == "-y" || a == "-hide_banner" || a == "-progress" || a == "pipe:1" {
			continue
		}
		if i > 0 && (step.Args[i-1] == "-i" || i == len(step.Args)-1) {
			parts = append(parts, filepath.Base(a))
			continue
		}
		if strings.ContainsAny(a, " \t,:") {
			parts = append(parts, `"`+a+`"`)
		} else {
			parts = append(parts, a)
		}
	}
	if len(plan.Steps) > 1 {
		parts = append(parts, fmt.Sprintf("(+%d more clips)", len(plan.Steps)-1))
	}
	return strings.Join(parts, " ")
}

func effectiveDuration(startSec, endSec float64, probe *MediaProbe) float64 {
	if endSec > startSec {
		return endSec - startSec
	}
	if probe != nil && probe.DurationSec > 0 {
		return probe.DurationSec
	}
	return 0
}

func baseNameNoExt(p string) string {
	b := filepath.Base(p)
	ext := filepath.Ext(b)
	return strings.TrimSuffix(b, ext)
}

func tempPathFor(finalPath string) string {
	ext := filepath.Ext(finalPath)
	base := strings.TrimSuffix(finalPath, ext)
	return base + ".part" + ext
}

func escapeConcatPath(p string) string {
	return strings.ReplaceAll(p, "'", "'\\''")
}

func escapeFilterPath(p string) string {
	s := strings.ReplaceAll(p, `\`, `/`)
	s = strings.ReplaceAll(s, `:`, `\:`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

func clampInt(v, def, minV, maxV int) int {
	if v == 0 {
		return def
	}
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}
