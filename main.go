package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"
	"github.com/seiortech/mtools-plugin-ffmpeg/internal/liveui"
	"github.com/seiortech/mtools-sdk/pluginapi"
)

type ffRunner struct {
	dataDir string
	app     *liveui.App
}

func (r *ffRunner) Inspect(ctx context.Context, params pluginapi.InspectParams) (pluginapi.InspectResult, error) {
	ffPath, ffErr := engine.ResolveFFmpeg(params.Settings["ffmpeg_path"], r.dataDir, params.Settings)
	fpPath, fpErr := engine.ResolveFFprobe(params.Settings["ffprobe_path"], r.dataDir, params.Settings)

	ffStatus := pluginapi.HelperStatus{Found: ffErr == nil, Path: ffPath}
	if ffErr != nil {
		ffStatus.Message = "ffmpeg not found — click Download FFmpeg."
	} else {
		ffStatus.Message = "ffmpeg ready (" + filepath.Base(ffPath) + ")"
	}

	fpStatus := pluginapi.HelperStatus{Found: fpErr == nil, Path: fpPath}
	if fpErr != nil {
		fpStatus.Message = "ffprobe not found — optional (falls back to ffmpeg -i)."
	} else {
		fpStatus.Message = "ffprobe ready (" + filepath.Base(fpPath) + ")"
	}

	return pluginapi.InspectResult{
		HelperStatus: map[string]pluginapi.HelperStatus{
			"ffmpeg":  ffStatus,
			"ffprobe": fpStatus,
		},
	}, nil
}

func (r *ffRunner) Run(ctx context.Context, params pluginapi.RunParams, emit func(pluginapi.Progress) error) (pluginapi.RunResult, error) {
	switch params.Action {
	case "fetch_ffmpeg":
		_ = emit(pluginapi.Progress{JobID: params.JobID, Done: 5, Total: 100, Unit: "%", Message: "Downloading ffmpeg..."})
		targetDir := firstInput(params.Inputs, "output_dir")
		if targetDir == "" {
			targetDir = params.SharedToolsDir()
		}
		if targetDir == "" {
			targetDir = filepath.Join(r.dataDir, "bin")
		}
		dest, err := engine.EnsureFFmpegInDir(ctx, targetDir, func(done, total int64) {
			pct := 50.0
			if total > 0 {
				pct = float64(done) / float64(total) * 100
			}
			_ = emit(pluginapi.Progress{JobID: params.JobID, Done: pct, Total: 100, Unit: "%", Message: fmt.Sprintf("Downloading ffmpeg %.0f%%", pct)})
		})
		if err != nil {
			return pluginapi.RunResult{JobID: params.JobID}, err
		}
		if r.app != nil {
			r.app.InvalidateBinaryCaches()
		}
		return pluginapi.RunResult{JobID: params.JobID, Outputs: []string{dest}, Summary: "Installed ffmpeg at " + dest}, nil

	case "fetch_ffprobe":
		_ = emit(pluginapi.Progress{JobID: params.JobID, Done: 5, Total: 100, Unit: "%", Message: "Downloading ffprobe..."})
		targetDir := firstInput(params.Inputs, "output_dir")
		if targetDir == "" {
			targetDir = params.SharedToolsDir()
		}
		if targetDir == "" {
			targetDir = filepath.Join(r.dataDir, "bin")
		}
		dest, err := engine.EnsureFFprobeInDir(ctx, targetDir, func(done, total int64) {
			pct := 50.0
			if total > 0 {
				pct = float64(done) / float64(total) * 100
			}
			_ = emit(pluginapi.Progress{JobID: params.JobID, Done: pct, Total: 100, Unit: "%", Message: fmt.Sprintf("Downloading ffprobe %.0f%%", pct)})
		})
		if err != nil {
			return pluginapi.RunResult{JobID: params.JobID}, err
		}
		if r.app != nil {
			r.app.InvalidateBinaryCaches()
		}
		return pluginapi.RunResult{JobID: params.JobID, Outputs: []string{dest}, Summary: "Installed ffprobe at " + dest}, nil

	case "transcode":
		ffPath, err := engine.ResolveFFmpeg(params.Settings["ffmpeg_path"], r.dataDir, params.Settings)
		if err != nil {
			return pluginapi.RunResult{JobID: params.JobID}, err
		}
		inFile := firstInput(params.Inputs, "input_file")
		outDir := firstInput(params.Inputs, "output_dir")
		if outDir == "" {
			outDir = params.Settings["output_dir"]
		}
		hwMode := engine.NormalizeHWAccelMode(params.Settings["hardware_accel"])
		hwBackend := engine.NormalizeHWBackend(params.Settings["hardware_backend"])
		hwCaps := engine.ProbeHWCapabilities(ctx, ffPath)
		hwPlan := engine.ResolveHWPlan(hwMode, hwBackend, hwCaps, runtime.GOOS)
		spec := engine.JobSpec{
			ID:         params.JobID,
			Mode:       engine.ModeConvert,
			InputPaths: []string{inFile},
			OutputDir:  outDir,
			HW:         hwPlan,
			Opts: engine.TranscodeOptions{
				Container:  params.Settings["default_container"],
				VideoCodec: "libx264",
				AudioCodec: "aac",
				CRF:        23,
				Overwrite:  params.Settings["overwrite_policy"] == "overwrite",
			},
		}
		plan, err := engine.BuildCommandPlan(spec, nil)
		if err != nil {
			return pluginapi.RunResult{JobID: params.JobID}, err
		}
		job := engine.Job{FFmpegPath: ffPath, Plan: plan, Overwrite: spec.Opts.Overwrite}
		res, err := job.Run(ctx, func(p pluginapi.Progress) {
			p.JobID = params.JobID
			_ = emit(p)
		})
		res.JobID = params.JobID
		return res, err

	default:
		return pluginapi.RunResult{JobID: params.JobID}, fmt.Errorf("unknown action: %s", params.Action)
	}
}

func firstInput(inputs map[string][]string, key string) string {
	vals := inputs[key]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func main() {
	dataDir := os.Getenv("MTOOLS_PLUGIN_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "mtools-ffmpeg")
	}

	app := liveui.NewApp(dataDir)
	defer app.Close()

	runner := &ffRunner{dataDir: dataDir, app: app}
	identity := pluginapi.InitializeResult{
		Protocol: pluginapi.Protocol,
		PluginID: "com.mtools.ffmpeg",
		Version:  "1.0.0",
	}

	if err := pluginapi.ServeLive(context.Background(), os.Stdin, os.Stdout, identity, runner, app); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
