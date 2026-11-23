package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/seiortech/mtools-sdk/pluginapi"
)

type Job struct {
	FFmpegPath string
	Plan       CommandPlan
	Overwrite  bool
}

// Run executes each CommandStep in Plan sequentially, emitting throttled progress and cleaning up partial files on cancel/failure.
func (j Job) Run(ctx context.Context, onProgress func(pluginapi.Progress)) (pluginapi.RunResult, error) {
	if strings.TrimSpace(j.FFmpegPath) == "" {
		return pluginapi.RunResult{}, ErrMissingFFmpeg
	}
	if len(j.Plan.Steps) == 0 {
		return pluginapi.RunResult{}, ErrNoInputFiles
	}

	var outputs []string
	totalSteps := len(j.Plan.Steps)

	for i, step := range j.Plan.Steps {
		if ctx.Err() != nil {
			return pluginapi.RunResult{}, ctx.Err()
		}

		finalOut := step.FinalOutput
		if step.MultiOutputDir != "" {
			if err := os.MkdirAll(step.MultiOutputDir, 0o755); err != nil {
				return pluginapi.RunResult{}, err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(finalOut), 0o755); err != nil {
				return pluginapi.RunResult{}, err
			}
			if !j.Overwrite {
				finalOut = uniqueOutputPath(finalOut)
			}
		}

		args := append([]string(nil), step.Args...)
		var concatFile string
		if step.ConcatListContent != "" && step.ConcatInputPlaceholder != "" {
			tf, err := os.CreateTemp("", "mtools-ffconcat-*.txt")
			if err != nil {
				return pluginapi.RunResult{}, err
			}
			concatFile = tf.Name()
			_, werr := tf.WriteString(step.ConcatListContent)
			_ = tf.Close()
			if werr != nil {
				_ = os.Remove(concatFile)
				return pluginapi.RunResult{}, werr
			}
			for k := range args {
				if args[k] == step.ConcatInputPlaceholder {
					args[k] = concatFile
				}
			}
		}

		err := j.runStep(ctx, args, step, i, totalSteps, onProgress)
		if concatFile != "" {
			_ = os.Remove(concatFile)
		}
		if err != nil {
			if step.MultiOutputDir == "" && step.TempOutput != "" {
				_ = os.Remove(step.TempOutput)
			}
			return pluginapi.RunResult{}, err
		}

		if step.MultiOutputDir == "" && step.TempOutput != "" {
			if err := os.Rename(step.TempOutput, finalOut); err != nil {
				_ = os.Remove(step.TempOutput)
				return pluginapi.RunResult{}, err
			}
		}
		outputs = append(outputs, finalOut)
	}

	return pluginapi.RunResult{
		Outputs: outputs,
		Summary: j.Plan.Summary,
	}, nil
}

func (j Job) runStep(
	ctx context.Context,
	args []string,
	step CommandStep,
	stepIdx, totalSteps int,
	onProgress func(pluginapi.Progress),
) error {
	cmd := exec.CommandContext(ctx, j.FFmpegPath, args...)
	prepareCommand(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stderrBuf, io.LimitReader(stderr, 64*1024))
	}()

	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		var st ProgressState
		var lastEmit time.Time
		for sc.Scan() {
			pct, detail, done, updated := ParseProgressKeyVal(sc.Text(), &st, step.DurationSec)
			if !updated || onProgress == nil {
				continue
			}
			overall := (float64(stepIdx)*100 + pct) / float64(totalSteps)
			if done || lastEmit.IsZero() || time.Since(lastEmit) >= 100*time.Millisecond {
				lastEmit = time.Now()
				msg := detail
				if totalSteps > 1 {
					msg = fmt.Sprintf("Clip %d/%d · %s", stepIdx+1, totalSteps, detail)
				}
				onProgress(pluginapi.Progress{
					Done:    overall,
					Total:   100,
					Unit:    "%",
					Message: msg,
				})
			}
		}
	}()

	wg.Wait()
	if err := cmd.Wait(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		msg := extractLastErrorLine(stderrBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffmpeg failed: %s", msg)
	}
	return nil
}

func uniqueOutputPath(target string) string {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return target
	}
	ext := filepath.Ext(target)
	base := strings.TrimSuffix(target, ext)
	for i := 1; i < 1000; i++ {
		cand := fmt.Sprintf("%s_%d%s", base, i, ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return target
}

func extractLastErrorLine(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l != "" {
			if len(l) > 220 {
				return l[:220]
			}
			return l
		}
	}
	return ""
}
