package engine

import (
	"errors"
	"slices"
	"testing"
)

func TestBuildCommandPlanAllModes(t *testing.T) {
	probe := &MediaProbe{Path: "/tmp/in.mp4", DurationSec: 60}

	// 1. Convert mode (Target size MB)
	plan, err := BuildCommandPlan(JobSpec{
		Mode:       ModeConvert,
		InputPaths: []string{"/tmp/in.mp4"},
		OutputDir:  "/tmp/out",
		Opts: TranscodeOptions{
			Container:    "mp4",
			VideoCodec:   "libx264",
			QualityMode:  "target_mb",
			TargetSizeMB: 24,
			ScaleHeight:  720,
		},
	}, probe)
	if err != nil {
		t.Fatalf("convert plan: %v", err)
	}
	if len(plan.Steps) != 1 || !slices.Contains(plan.Steps[0].Args, "-b:v") {
		t.Fatalf("expected -b:v in target_mb mode, got %v", plan.Steps[0].Args)
	}

	// 2. Incompatible codec/container rejected
	_, err = BuildCommandPlan(JobSpec{
		Mode:       ModeConvert,
		InputPaths: []string{"/tmp/in.mp4"},
		OutputDir:  "/tmp/out",
		Opts: TranscodeOptions{
			Container:  "webm",
			VideoCodec: "libx264",
		},
	}, probe)
	if !errors.Is(err, ErrUnsupportedCodecContainer) {
		t.Fatalf("expected ErrUnsupportedCodecContainer, got %v", err)
	}

	// 3. Trim mode (multi-clip)
	trimPlan, err := BuildCommandPlan(JobSpec{
		Mode:       ModeTrim,
		InputPaths: []string{"/tmp/in.mp4"},
		OutputDir:  "/tmp/out",
		Clips: []ClipSegment{
			{ID: "c1", StartSec: 5, EndSec: 15},
			{ID: "c2", StartSec: 20, EndSec: 30},
		},
		Opts: TranscodeOptions{LosslessCut: true, Container: "mp4"},
	}, probe)
	if err != nil {
		t.Fatalf("trim plan: %v", err)
	}
	if len(trimPlan.Steps) != 2 || !slices.Contains(trimPlan.Steps[0].Args, "copy") {
		t.Fatalf("expected 2 lossless copy steps, got %+v", trimPlan.Steps)
	}

	// 4. Streams mode (loudnorm + strip metadata)
	stPlan, err := BuildCommandPlan(JobSpec{
		Mode:       ModeStreams,
		InputPaths: []string{"/tmp/in.mp4"},
		OutputDir:  "/tmp/out",
		Opts: TranscodeOptions{
			Container:     "mp4",
			AudioTrackIdx: 0,
			Loudnorm:      true,
			StripMetadata: true,
			SubMode:       "strip",
		},
	}, probe)
	if err != nil {
		t.Fatalf("streams plan: %v", err)
	}
	if !slices.Contains(stPlan.Steps[0].Args, "-sn") || !slices.Contains(stPlan.Steps[0].Args, "-map_metadata") {
		t.Fatalf("expected -sn and -map_metadata in streams args: %v", stPlan.Steps[0].Args)
	}

	// 5. Frames mode (2-pass GIF & contact sheet)
	gifPlan, err := BuildCommandPlan(JobSpec{
		Mode:       ModeFrames,
		InputPaths: []string{"/tmp/in.mp4"},
		StartSec:   2,
		EndSec:     8,
		OutputDir:  "/tmp/out",
		Opts: TranscodeOptions{
			FramesOp: "gif",
			GIFFPS:   15,
			GIFWidth: 480,
		},
	}, probe)
	if err != nil {
		t.Fatalf("gif plan: %v", err)
	}
	if len(gifPlan.Steps) != 1 || gifPlan.Steps[0].DurationSec != 6 {
		t.Fatalf("unexpected gif plan: %+v", gifPlan)
	}
}

func TestBuildCommandPlanHardwareAccelAndFallback(t *testing.T) {
	probe := &MediaProbe{Path: "/tmp/in.mp4", DurationSec: 60}

	// 1. VideoToolbox GPU decode + encode + pure CPU FallbackArgs
	vtPlan := HWPlan{
		Mode:          HWAccelAuto,
		Backend:       HWBackendVideoToolbox,
		DecodeMethod:  "videotoolbox",
		H264Encoder:   "h264_videotoolbox",
		HEVCEncoder:   "hevc_videotoolbox",
		ProResEncoder: "prores_videotoolbox",
	}
	plan, err := BuildCommandPlan(JobSpec{
		Mode:       ModeConvert,
		InputPaths: []string{"/tmp/in.mp4"},
		OutputDir:  "/tmp/out",
		Opts: TranscodeOptions{
			Container:  "mp4",
			VideoCodec: "libx264",
			CRF:        23,
		},
		HW: vtPlan,
	}, probe)
	if err != nil {
		t.Fatalf("vt convert plan: %v", err)
	}
	args := plan.Steps[0].Args
	fb := plan.Steps[0].FallbackArgs
	if !slices.Contains(args, "-hwaccel") || !slices.Contains(args, "videotoolbox") || !slices.Contains(args, "h264_videotoolbox") || !slices.Contains(args, "-allow_sw") {
		t.Fatalf("expected videotoolbox hwaccel & encoder in args: %v", args)
	}
	if len(fb) == 0 || slices.Contains(fb, "-hwaccel") || !slices.Contains(fb, "libx264") {
		t.Fatalf("expected pure CPU libx264 FallbackArgs, got %v", fb)
	}

	// 2. NVENC, QSV, AMF, VA-API encoder mappings
	for _, tc := range []struct {
		name    string
		hw      HWPlan
		wantEnc string
		wantArg string
	}{
		{
			name:    "nvenc",
			hw:      HWPlan{Mode: HWAccelAuto, Backend: HWBackendCUDA, DecodeMethod: "cuda", H264Encoder: "h264_nvenc"},
			wantEnc: "h264_nvenc",
			wantArg: "-cq",
		},
		{
			name:    "qsv",
			hw:      HWPlan{Mode: HWAccelAuto, Backend: HWBackendQSV, DecodeMethod: "qsv", H264Encoder: "h264_qsv"},
			wantEnc: "h264_qsv",
			wantArg: "-global_quality",
		},
		{
			name:    "amf",
			hw:      HWPlan{Mode: HWAccelAuto, Backend: HWBackendAMF, DecodeMethod: "d3d11va", H264Encoder: "h264_amf"},
			wantEnc: "h264_amf",
			wantArg: "-qp_i",
		},
		{
			name:    "vaapi",
			hw:      HWPlan{Mode: HWAccelAuto, Backend: HWBackendVAAPI, DecodeMethod: "vaapi", DecodeDevice: "/dev/dri/renderD128", H264Encoder: "h264_vaapi"},
			wantEnc: "h264_vaapi",
			wantArg: "hwupload",
		},
	} {
		p, err := BuildCommandPlan(JobSpec{
			Mode:       ModeConvert,
			InputPaths: []string{"/tmp/in.mp4"},
			OutputDir:  "/tmp/out",
			Opts:       TranscodeOptions{Container: "mp4", VideoCodec: "libx264", CRF: 22},
			HW:         tc.hw,
		}, probe)
		if err != nil {
			t.Fatalf("%s plan: %v", tc.name, err)
		}
		if !slices.Contains(p.Steps[0].Args, tc.wantEnc) {
			t.Fatalf("%s: expected encoder %s in %v", tc.name, tc.wantEnc, p.Steps[0].Args)
		}
		foundArg := false
		for _, a := range p.Steps[0].Args {
			if a == tc.wantArg || (tc.wantArg == "hwupload" && slices.Contains([]string{a}, a) && len(a) >= len("hwupload") && (a == "format=nv12,hwupload")) {
				foundArg = true
			}
		}
		if !foundArg {
			t.Fatalf("%s: expected %q in %v", tc.name, tc.wantArg, p.Steps[0].Args)
		}
	}

	// 3. DecodeOnly uses -hwaccel but keeps CPU libx264 encoder
	decOnly := HWPlan{Mode: HWAccelDecodeOnly, Backend: HWBackendVideoToolbox, DecodeMethod: "videotoolbox"}
	decPlan, err := BuildCommandPlan(JobSpec{
		Mode:       ModeConvert,
		InputPaths: []string{"/tmp/in.mp4"},
		OutputDir:  "/tmp/out",
		Opts:       TranscodeOptions{Container: "mp4", VideoCodec: "libx264", CRF: 23},
		HW:         decOnly,
	}, probe)
	if err != nil {
		t.Fatalf("decode_only plan: %v", err)
	}
	if !slices.Contains(decPlan.Steps[0].Args, "-hwaccel") || !slices.Contains(decPlan.Steps[0].Args, "libx264") {
		t.Fatalf("expected -hwaccel + libx264 in decode_only, got %v", decPlan.Steps[0].Args)
	}

	// 4. Lossless trim copy skips -hwaccel and has no FallbackArgs
	losslessPlan, err := BuildCommandPlan(JobSpec{
		Mode:       ModeTrim,
		InputPaths: []string{"/tmp/in.mp4"},
		StartSec:   1,
		EndSec:     10,
		OutputDir:  "/tmp/out",
		Opts:       TranscodeOptions{LosslessCut: true, Container: "mp4"},
		HW:         vtPlan,
	}, probe)
	if err != nil {
		t.Fatalf("lossless trim plan: %v", err)
	}
	if slices.Contains(losslessPlan.Steps[0].Args, "-hwaccel") || len(losslessPlan.Steps[0].FallbackArgs) != 0 {
		t.Fatalf("expected lossless trim copy to skip -hwaccel and FallbackArgs, got args=%v fb=%v", losslessPlan.Steps[0].Args, losslessPlan.Steps[0].FallbackArgs)
	}
}
