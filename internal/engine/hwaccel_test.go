package engine

import (
	"slices"
	"strings"
	"testing"
)

func TestHWCapabilitiesParsingAndResolveHWPlan(t *testing.T) {
	hwAccelsOut := `
Hardware acceleration methods:
videotoolbox
`
	accels := ParseHWAccelsOutput(hwAccelsOut)
	if !accels["videotoolbox"] || accels["methods:"] {
		t.Fatalf("unexpected parsed hwaccels: %+v", accels)
	}

	encodersOut := `
Encoders:
 V..... libx264              libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)
 V....D h264_videotoolbox    VideoToolbox H.264 Encoder (codec h264)
 V....D hevc_videotoolbox    VideoToolbox H.265 Encoder (codec hevc)
 V....D prores_videotoolbox  VideoToolbox ProRes Encoder (codec prores)
`
	encoders := ParseEncodersOutput(encodersOut)
	for _, want := range []string{"libx264", "h264_videotoolbox", "hevc_videotoolbox", "prores_videotoolbox"} {
		if !encoders[want] {
			t.Fatalf("expected encoder %q in %+v", want, encoders)
		}
	}

	// 1. macOS auto -> VideoToolbox decode + encode
	macPlan := ResolveHWPlan("auto", "auto", HWCapabilities{Accels: accels, Encoders: encoders}, "darwin")
	if macPlan.DecodeMethod != "videotoolbox" || macPlan.H264Encoder != "h264_videotoolbox" || macPlan.HEVCEncoder != "hevc_videotoolbox" {
		t.Fatalf("unexpected darwin auto plan: %+v", macPlan)
	}

	// 2. macOS decode_only -> decode only, no HW encoders
	decOnly := ResolveHWPlan("decode_only", "auto", HWCapabilities{Accels: accels, Encoders: encoders}, "darwin")
	if decOnly.DecodeMethod != "videotoolbox" || decOnly.H264Encoder != "" || decOnly.HEVCEncoder != "" {
		t.Fatalf("unexpected decode_only plan: %+v", decOnly)
	}

	// 3. Off -> empty decode & encode
	offPlan := ResolveHWPlan("off", "auto", HWCapabilities{Accels: accels, Encoders: encoders}, "darwin")
	if offPlan.Active() || offPlan.DecodeMethod != "" || offPlan.H264Encoder != "" {
		t.Fatalf("expected inactive plan for off mode, got %+v", offPlan)
	}

	// 4. Windows NVIDIA CUDA/NVENC auto
	winCaps := HWCapabilities{
		Accels:   map[string]bool{"d3d11va": true, "cuda": true, "qsv": true},
		Encoders: map[string]bool{"h264_nvenc": true, "hevc_nvenc": true, "h264_qsv": true, "h264_amf": true},
	}
	winAuto := ResolveHWPlan("auto", "auto", winCaps, "windows")
	if winAuto.Backend != HWBackendCUDA || winAuto.DecodeMethod != "cuda" || winAuto.H264Encoder != "h264_nvenc" {
		t.Fatalf("unexpected windows auto plan: %+v", winAuto)
	}
	winQSV := ResolveHWPlan("auto", "qsv", winCaps, "windows")
	if winQSV.DecodeMethod != "qsv" || winQSV.H264Encoder != "h264_qsv" {
		t.Fatalf("unexpected windows qsv plan: %+v", winQSV)
	}
	winAMF := ResolveHWPlan("auto", "amf", winCaps, "windows")
	if winAMF.DecodeMethod != "d3d11va" || winAMF.H264Encoder != "h264_amf" {
		t.Fatalf("unexpected windows amf plan: %+v", winAMF)
	}

	// 5. Linux VAAPI with render device
	linCaps := HWCapabilities{
		Accels:   map[string]bool{"vaapi": true},
		Encoders: map[string]bool{"h264_vaapi": true, "hevc_vaapi": true},
		VAAPIDev: "/dev/dri/renderD128",
	}
	linPlan := ResolveHWPlan("auto", "vaapi", linCaps, "linux")
	if linPlan.DecodeMethod != "vaapi" || linPlan.DecodeDevice != "/dev/dri/renderD128" || linPlan.H264Encoder != "h264_vaapi" {
		t.Fatalf("unexpected linux vaapi plan: %+v", linPlan)
	}
	prefix := DecodeInputPrefix(linPlan)
	if !slices.Contains(prefix, "-vaapi_device") || !slices.Contains(prefix, "/dev/dri/renderD128") {
		t.Fatalf("expected -vaapi_device in linux prefix, got %v", prefix)
	}

	// 6. Unavailable explicit backend safely falls back to inactive CPU label
	unavail := ResolveHWPlan("auto", "cuda", HWCapabilities{Accels: accels, Encoders: encoders}, "darwin")
	if unavail.Active() || !strings.Contains(unavail.SummaryLabel, "unavailable") {
		t.Fatalf("expected unavailable fallback label for cuda on darwin, got %+v", unavail)
	}
}
