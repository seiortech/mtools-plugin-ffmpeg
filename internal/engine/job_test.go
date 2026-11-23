package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestParseProgressKeyVal(t *testing.T) {
	var st ProgressState
	_, _, _, upd := ParseProgressKeyVal("out_time_us=15000000", &st, 30)
	if upd {
		t.Fatalf("expected updated=false before progress= line")
	}
	_, _, _, _ = ParseProgressKeyVal("speed=2.5x", &st, 30)
	pct, detail, done, upd := ParseProgressKeyVal("progress=continue", &st, 30)
	if !upd || done || pct != 50 {
		t.Fatalf("expected 50%% continue, got pct=%v done=%v upd=%v", pct, done, upd)
	}
	if detail == "" {
		t.Fatalf("expected non-empty detail")
	}
	pct, _, done, _ = ParseProgressKeyVal("progress=end", &st, 30)
	if !done || pct != 100 {
		t.Fatalf("expected done=true pct=100, got done=%v pct=%v", done, pct)
	}
}

func TestJobCancelCleansPartialFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture uses sh")
	}
	tmp := t.TempDir()
	fakeFF := filepath.Join(tmp, "ffmpeg")
	outFile := filepath.Join(tmp, "out.mp4")
	partFile := tempPathFor(outFile)

	script := "#!/bin/sh\ntouch \"" + partFile + "\"\necho 'out_time_us=1000000'\necho 'progress=continue'\nsleep 5\n"
	if err := os.WriteFile(fakeFF, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	job := Job{
		FFmpegPath: fakeFF,
		Plan: CommandPlan{
			Steps: []CommandStep{{
				Args:        []string{"-version"},
				TempOutput:  partFile,
				FinalOutput: outFile,
				DurationSec: 10,
			}},
			Summary: "done",
		},
	}

	_, err := job.Run(ctx, nil)
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && err == nil {
		t.Fatalf("expected cancellation/timeout error, got %v", err)
	}
	if _, statErr := os.Stat(partFile); !os.IsNotExist(statErr) {
		t.Fatalf("expected partial file %s to be removed on cancel", partFile)
	}
}

func TestJobRunFallsBackToCPUOnHardwareFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture uses sh")
	}
	tmp := t.TempDir()
	fakeFF := filepath.Join(tmp, "ffmpeg")
	outFile := filepath.Join(tmp, "out.mp4")
	partFile := tempPathFor(outFile)

	// Fail when invoked with -hwaccel, succeed on CPU fallback.
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"-hwaccel\" ]; then\n    echo 'hw init failed' >&2\n    exit 1\n  fi\ndone\ntouch \"" + partFile + "\"\necho 'progress=end'\nexit 0\n"
	if err := os.WriteFile(fakeFF, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	job := Job{
		FFmpegPath: fakeFF,
		Plan: CommandPlan{
			Steps: []CommandStep{{
				Args:         []string{"-hwaccel", "videotoolbox", partFile},
				FallbackArgs: []string{"-c:v", "libx264", partFile},
				TempOutput:   partFile,
				FinalOutput:  outFile,
				DurationSec:  10,
			}},
			Summary: "done",
		},
	}

	res, err := job.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected CPU fallback to succeed, got %v", err)
	}
	if len(res.Outputs) != 1 || res.Outputs[0] != outFile {
		t.Fatalf("unexpected outputs: %+v", res.Outputs)
	}
	if _, statErr := os.Stat(outFile); statErr != nil {
		t.Fatalf("expected final output %s on disk: %v", outFile, statErr)
	}

	// Canceled context must NOT retry on CPU fallback.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, cerr := job.Run(cctx, nil); !errors.Is(cerr, context.Canceled) {
		t.Fatalf("expected context.Canceled without fallback, got %v", cerr)
	}
}
