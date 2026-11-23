package engine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	HWAccelAuto       = "auto"
	HWAccelDecodeOnly = "decode_only"
	HWAccelOff        = "off"

	HWBackendAuto         = "auto"
	HWBackendVideoToolbox = "videotoolbox"
	HWBackendD3D11VA      = "d3d11va"
	HWBackendCUDA         = "cuda"
	HWBackendQSV          = "qsv"
	HWBackendAMF          = "amf"
	HWBackendVAAPI        = "vaapi"
)

// HWCapabilities holds the hardware acceleration decoders and encoders reported by ffmpeg.
type HWCapabilities struct {
	Accels   map[string]bool
	Encoders map[string]bool
	VAAPIDev string
}

// HWPlan describes the resolved hardware decode and encode configuration for FFmpeg commands.
type HWPlan struct {
	Mode          string
	Backend       string
	DecodeMethod  string // e.g. "videotoolbox", "d3d11va", "cuda", "qsv", "vaapi"
	DecodeDevice  string // e.g. "/dev/dri/renderD128" for vaapi
	H264Encoder   string // e.g. "h264_videotoolbox", "h264_nvenc", "h264_qsv", "h264_amf", "h264_vaapi"
	HEVCEncoder   string // e.g. "hevc_videotoolbox", "hevc_nvenc", "hevc_qsv", "hevc_amf", "hevc_vaapi"
	ProResEncoder string // e.g. "prores_videotoolbox"
	SummaryLabel  string
}

func (p HWPlan) Active() bool {
	return p.Mode != HWAccelOff && (p.DecodeMethod != "" || p.H264Encoder != "" || p.HEVCEncoder != "" || p.ProResEncoder != "")
}

func NormalizeHWAccelMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case HWAccelDecodeOnly, HWAccelOff:
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return HWAccelAuto
	}
}

func NormalizeHWBackend(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case HWBackendVideoToolbox, HWBackendD3D11VA, HWBackendCUDA, HWBackendQSV, HWBackendAMF, HWBackendVAAPI:
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return HWBackendAuto
	}
}

// ParseHWAccelsOutput parses `ffmpeg -hide_banner -hwaccels` output into a set of accel names.
func ParseHWAccelsOutput(out string) map[string]bool {
	res := make(map[string]bool)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasSuffix(line, ":") || strings.Contains(line, " ") {
			continue
		}
		res[strings.ToLower(line)] = true
	}
	return res
}

// ParseEncodersOutput parses `ffmpeg -hide_banner -encoders` output into a set of encoder names.
func ParseEncodersOutput(out string) map[string]bool {
	res := make(map[string]bool)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "--") || strings.HasSuffix(line, ":") || strings.HasPrefix(line, "Encoders:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && len(fields[0]) == 6 {
			res[strings.ToLower(fields[1])] = true
		}
	}
	return res
}

// ProbeHWCapabilities inspects ffmpeg -hwaccels and -encoders with a bounded timeout.
func ProbeHWCapabilities(ctx context.Context, ffmpegBin string) HWCapabilities {
	caps := HWCapabilities{
		Accels:   make(map[string]bool),
		Encoders: make(map[string]bool),
	}
	if strings.TrimSpace(ffmpegBin) == "" {
		return caps
	}

	pctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	var hwOut bytes.Buffer
	hwCmd := exec.CommandContext(pctx, ffmpegBin, "-hide_banner", "-hwaccels")
	prepareCommand(hwCmd)
	hwCmd.Stdout = &hwOut
	if err := hwCmd.Run(); err == nil {
		caps.Accels = ParseHWAccelsOutput(hwOut.String())
	}

	var encOut bytes.Buffer
	encCmd := exec.CommandContext(pctx, ffmpegBin, "-hide_banner", "-encoders")
	prepareCommand(encCmd)
	encCmd.Stdout = &encOut
	if err := encCmd.Run(); err == nil {
		caps.Encoders = ParseEncodersOutput(encOut.String())
	}

	if runtime.GOOS == "linux" && caps.Accels["vaapi"] {
		if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
			caps.VAAPIDev = "/dev/dri/renderD128"
		}
	}
	return caps
}

// ResolveHWPlan selects the best decode/encode hardware configuration for the OS, user settings, and probed FFmpeg capabilities.
func ResolveHWPlan(mode, backend string, caps HWCapabilities, goos string) HWPlan {
	m := NormalizeHWAccelMode(mode)
	b := NormalizeHWBackend(backend)
	if m == HWAccelOff {
		return HWPlan{
			Mode:         HWAccelOff,
			Backend:      b,
			SummaryLabel: "Software CPU (Off)",
		}
	}

	candidates := backendOrder(b, goos)
	for _, cand := range candidates {
		plan, ok := resolveSingleBackend(m, cand, caps, goos)
		if ok {
			return plan
		}
	}

	if b != HWBackendAuto {
		return HWPlan{
			Mode:         m,
			Backend:      b,
			SummaryLabel: fmt.Sprintf("%s unavailable — using CPU", backendDisplayName(b)),
		}
	}
	return HWPlan{
		Mode:         m,
		Backend:      HWBackendAuto,
		SummaryLabel: "Software CPU (no GPU backend detected)",
	}
}

func backendOrder(preferred, goos string) []string {
	if preferred != HWBackendAuto {
		return []string{preferred}
	}
	switch goos {
	case "darwin":
		return []string{HWBackendVideoToolbox}
	case "windows":
		return []string{HWBackendCUDA, HWBackendQSV, HWBackendAMF, HWBackendD3D11VA}
	case "linux":
		return []string{HWBackendCUDA, HWBackendQSV, HWBackendVAAPI}
	default:
		return nil
	}
}

func resolveSingleBackend(mode, backend string, caps HWCapabilities, goos string) (HWPlan, bool) {
	plan := HWPlan{Mode: mode, Backend: backend}
	switch backend {
	case HWBackendVideoToolbox:
		if goos != "darwin" {
			return HWPlan{}, false
		}
		if caps.Accels["videotoolbox"] {
			plan.DecodeMethod = "videotoolbox"
		}
		if mode == HWAccelAuto {
			if caps.Encoders["h264_videotoolbox"] {
				plan.H264Encoder = "h264_videotoolbox"
			}
			if caps.Encoders["hevc_videotoolbox"] {
				plan.HEVCEncoder = "hevc_videotoolbox"
			}
			if caps.Encoders["prores_videotoolbox"] {
				plan.ProResEncoder = "prores_videotoolbox"
			}
		}
	case HWBackendD3D11VA:
		if goos != "windows" {
			return HWPlan{}, false
		}
		if caps.Accels["d3d11va"] {
			plan.DecodeMethod = "d3d11va"
		} else if caps.Accels["dxva2"] {
			plan.DecodeMethod = "dxva2"
		}
		if mode == HWAccelAuto {
			// D3D11VA is decode-only; pair with any available Windows GPU encoder if present.
			switch {
			case caps.Encoders["h264_nvenc"] || caps.Encoders["hevc_nvenc"]:
				if caps.Encoders["h264_nvenc"] {
					plan.H264Encoder = "h264_nvenc"
				}
				if caps.Encoders["hevc_nvenc"] {
					plan.HEVCEncoder = "hevc_nvenc"
				}
			case caps.Encoders["h264_qsv"] || caps.Encoders["hevc_qsv"]:
				if caps.Encoders["h264_qsv"] {
					plan.H264Encoder = "h264_qsv"
				}
				if caps.Encoders["hevc_qsv"] {
					plan.HEVCEncoder = "hevc_qsv"
				}
			case caps.Encoders["h264_amf"] || caps.Encoders["hevc_amf"]:
				if caps.Encoders["h264_amf"] {
					plan.H264Encoder = "h264_amf"
				}
				if caps.Encoders["hevc_amf"] {
					plan.HEVCEncoder = "hevc_amf"
				}
			}
		}
	case HWBackendCUDA:
		if goos == "darwin" {
			return HWPlan{}, false
		}
		if caps.Accels["cuda"] {
			plan.DecodeMethod = "cuda"
		} else if goos == "windows" && caps.Accels["d3d11va"] {
			plan.DecodeMethod = "d3d11va"
		}
		if mode == HWAccelAuto {
			if caps.Encoders["h264_nvenc"] {
				plan.H264Encoder = "h264_nvenc"
			}
			if caps.Encoders["hevc_nvenc"] {
				plan.HEVCEncoder = "hevc_nvenc"
			}
		}
	case HWBackendQSV:
		if goos == "darwin" {
			return HWPlan{}, false
		}
		if caps.Accels["qsv"] {
			plan.DecodeMethod = "qsv"
		} else if goos == "windows" && caps.Accels["d3d11va"] {
			plan.DecodeMethod = "d3d11va"
		}
		if mode == HWAccelAuto {
			if caps.Encoders["h264_qsv"] {
				plan.H264Encoder = "h264_qsv"
			}
			if caps.Encoders["hevc_qsv"] {
				plan.HEVCEncoder = "hevc_qsv"
			}
		}
	case HWBackendAMF:
		if goos != "windows" {
			return HWPlan{}, false
		}
		if caps.Accels["d3d11va"] {
			plan.DecodeMethod = "d3d11va"
		} else if caps.Accels["dxva2"] {
			plan.DecodeMethod = "dxva2"
		}
		if mode == HWAccelAuto {
			if caps.Encoders["h264_amf"] {
				plan.H264Encoder = "h264_amf"
			}
			if caps.Encoders["hevc_amf"] {
				plan.HEVCEncoder = "hevc_amf"
			}
		}
	case HWBackendVAAPI:
		if goos != "linux" {
			return HWPlan{}, false
		}
		dev := caps.VAAPIDev
		if dev == "" && caps.Accels["vaapi"] {
			dev = "/dev/dri/renderD128"
		}
		if caps.Accels["vaapi"] && dev != "" {
			plan.DecodeMethod = "vaapi"
			plan.DecodeDevice = dev
		}
		if mode == HWAccelAuto {
			if caps.Encoders["h264_vaapi"] && dev != "" {
				plan.H264Encoder = "h264_vaapi"
				plan.DecodeDevice = dev
			}
			if caps.Encoders["hevc_vaapi"] && dev != "" {
				plan.HEVCEncoder = "hevc_vaapi"
				plan.DecodeDevice = dev
			}
		}
	}

	if !plan.Active() {
		return HWPlan{}, false
	}
	hasEnc := plan.H264Encoder != "" || plan.HEVCEncoder != "" || plan.ProResEncoder != ""
	switch {
	case mode == HWAccelDecodeOnly || !hasEnc:
		plan.SummaryLabel = backendDisplayName(backend) + " (GPU Decode + CPU Encode)"
	case plan.DecodeMethod != "":
		plan.SummaryLabel = backendDisplayName(backend) + " (GPU Decode + Encode)"
	default:
		plan.SummaryLabel = backendDisplayName(backend) + " (GPU Encode)"
	}
	return plan, true
}

func backendDisplayName(b string) string {
	switch b {
	case HWBackendVideoToolbox:
		return "Apple VideoToolbox"
	case HWBackendD3D11VA:
		return "Direct3D 11 (D3D11VA)"
	case HWBackendCUDA:
		return "NVIDIA CUDA / NVENC"
	case HWBackendQSV:
		return "Intel Quick Sync (QSV)"
	case HWBackendAMF:
		return "AMD AMF"
	case HWBackendVAAPI:
		return "Linux VA-API"
	default:
		return "Auto"
	}
}

// DecodeInputPrefix returns the FFmpeg input arguments that enable hardware decoding before `-i`.
func DecodeInputPrefix(hw HWPlan) []string {
	if hw.Mode == HWAccelOff || hw.DecodeMethod == "" {
		return nil
	}
	if hw.DecodeMethod == "vaapi" && hw.DecodeDevice != "" {
		return []string{"-hwaccel", "vaapi", "-vaapi_device", hw.DecodeDevice}
	}
	return []string{"-hwaccel", hw.DecodeMethod}
}

// MapVideoEncoder resolves requestedCodec (e.g. "libx264") to an active GPU encoder when enabled.
func MapVideoEncoder(requestedCodec string, hw HWPlan) (codec string, isHW bool) {
	if hw.Mode != HWAccelAuto {
		return requestedCodec, false
	}
	switch requestedCodec {
	case "libx264":
		if hw.H264Encoder != "" {
			return hw.H264Encoder, true
		}
	case "libx265":
		if hw.HEVCEncoder != "" {
			return hw.HEVCEncoder, true
		}
	case "prores_ks":
		if hw.ProResEncoder != "" {
			return hw.ProResEncoder, true
		}
	}
	return requestedCodec, false
}

// IsHWEncoder reports whether codec is one of the supported hardware video encoders.
func IsHWEncoder(codec string) bool {
	return strings.HasSuffix(codec, "_videotoolbox") ||
		strings.HasSuffix(codec, "_nvenc") ||
		strings.HasSuffix(codec, "_qsv") ||
		strings.HasSuffix(codec, "_amf") ||
		strings.HasSuffix(codec, "_vaapi")
}

// AppendVideoEncoderArgs appends backend-appropriate encoder, quality, pixel format, and filter flags.
func AppendVideoEncoderArgs(
	args []string,
	codec string,
	container string,
	crf int,
	encoderPreset string,
	scaleHeight int,
	bitrateKbps int,
	maxRateKbps int,
	bufSizeKbps int,
	hw HWPlan,
) []string {
	isVAAPI := strings.HasSuffix(codec, "_vaapi")
	if isVAAPI && hw.DecodeDevice != "" && hw.DecodeMethod != "vaapi" {
		args = append(args, "-vaapi_device", hw.DecodeDevice)
	}

	var vfParts []string
	if scaleHeight > 0 {
		vfParts = append(vfParts, fmt.Sprintf("scale=-2:min(ih\\,%d)", scaleHeight))
	}
	if isVAAPI {
		vfParts = append(vfParts, "format=nv12", "hwupload")
	}
	if len(vfParts) > 0 {
		args = append(args, "-vf", strings.Join(vfParts, ","))
	}

	args = append(args, "-c:v", codec)
	if crf <= 0 {
		crf = 23
	}

	switch {
	case strings.HasSuffix(codec, "_videotoolbox"):
		args = append(args, "-allow_sw", "1")
		if codec == "prores_videotoolbox" {
			args = append(args, "-profile:v", "2")
		} else if bitrateKbps > 0 {
			args = append(args, "-b:v", fmt.Sprintf("%dk", bitrateKbps))
			if maxRateKbps > 0 {
				args = append(args, "-maxrate", fmt.Sprintf("%dk", maxRateKbps), "-bufsize", fmt.Sprintf("%dk", bufSizeKbps))
			}
		} else {
			args = append(args, "-q:v", strconv.Itoa(crfToVideoToolboxQuality(crf)))
		}
		if codec == "hevc_videotoolbox" && (container == "mp4" || container == "mov") {
			args = append(args, "-tag:v", "hvc1")
		}
		return args

	case strings.HasSuffix(codec, "_nvenc"):
		if encoderPreset != "" {
			args = append(args, "-preset", mapNVENCPre(encoderPreset))
		}
		args = append(args, "-pix_fmt", "yuv420p")
		if bitrateKbps > 0 {
			args = append(args, "-b:v", fmt.Sprintf("%dk", bitrateKbps))
			if maxRateKbps > 0 {
				args = append(args, "-maxrate", fmt.Sprintf("%dk", maxRateKbps), "-bufsize", fmt.Sprintf("%dk", bufSizeKbps))
			}
		} else {
			args = append(args, "-rc", "vbr", "-cq", strconv.Itoa(crf), "-b:v", "0")
		}
		if codec == "hevc_nvenc" && (container == "mp4" || container == "mov") {
			args = append(args, "-tag:v", "hvc1")
		}
		return args

	case strings.HasSuffix(codec, "_qsv"):
		if encoderPreset != "" {
			args = append(args, "-preset", mapQSVPre(encoderPreset))
		}
		args = append(args, "-pix_fmt", "nv12")
		if bitrateKbps > 0 {
			args = append(args, "-b:v", fmt.Sprintf("%dk", bitrateKbps))
			if maxRateKbps > 0 {
				args = append(args, "-maxrate", fmt.Sprintf("%dk", maxRateKbps), "-bufsize", fmt.Sprintf("%dk", bufSizeKbps))
			}
		} else {
			args = append(args, "-global_quality", strconv.Itoa(crf))
		}
		if codec == "hevc_qsv" && (container == "mp4" || container == "mov") {
			args = append(args, "-tag:v", "hvc1")
		}
		return args

	case strings.HasSuffix(codec, "_amf"):
		args = append(args, "-pix_fmt", "yuv420p")
		if bitrateKbps > 0 {
			args = append(args, "-b:v", fmt.Sprintf("%dk", bitrateKbps))
			if maxRateKbps > 0 {
				args = append(args, "-maxrate", fmt.Sprintf("%dk", maxRateKbps), "-bufsize", fmt.Sprintf("%dk", bufSizeKbps))
			}
		} else {
			q := strconv.Itoa(crf)
			args = append(args, "-rc", "cqp", "-qp_i", q, "-qp_p", q)
		}
		if codec == "hevc_amf" && (container == "mp4" || container == "mov") {
			args = append(args, "-tag:v", "hvc1")
		}
		return args

	case isVAAPI:
		if bitrateKbps > 0 {
			args = append(args, "-b:v", fmt.Sprintf("%dk", bitrateKbps))
			if maxRateKbps > 0 {
				args = append(args, "-maxrate", fmt.Sprintf("%dk", maxRateKbps), "-bufsize", fmt.Sprintf("%dk", bufSizeKbps))
			}
		} else {
			args = append(args, "-qp", strconv.Itoa(crf))
		}
		return args

	default:
		if codec == "libx264" || codec == "libx265" {
			args = append(args, "-pix_fmt", "yuv420p")
			if encoderPreset != "" {
				args = append(args, "-preset", encoderPreset)
			}
		}
		if codec == "libx265" && container == "mp4" {
			args = append(args, "-tag:v", "hvc1")
		}
		if bitrateKbps > 0 {
			args = append(args, "-b:v", fmt.Sprintf("%dk", bitrateKbps))
			if maxRateKbps > 0 {
				args = append(args, "-maxrate", fmt.Sprintf("%dk", maxRateKbps), "-bufsize", fmt.Sprintf("%dk", bufSizeKbps))
			}
		} else {
			args = append(args, "-crf", strconv.Itoa(crf))
		}
		return args
	}
}

func crfToVideoToolboxQuality(crf int) int {
	// Map CRF 14..36 to VideoToolbox -q:v 85..35 (higher is better quality).
	q := 85 - ((crf - 14) * 50 / 22)
	if q < 35 {
		return 35
	}
	if q > 85 {
		return 85
	}
	return q
}

func mapNVENCPre(preset string) string {
	switch preset {
	case "ultrafast":
		return "p1"
	case "fast":
		return "p3"
	case "slow":
		return "p6"
	default:
		return "p4"
	}
}

func mapQSVPre(preset string) string {
	switch preset {
	case "ultrafast":
		return "veryfast"
	case "fast":
		return "faster"
	case "slow":
		return "slower"
	default:
		return "medium"
	}
}
