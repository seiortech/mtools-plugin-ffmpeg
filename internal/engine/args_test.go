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
