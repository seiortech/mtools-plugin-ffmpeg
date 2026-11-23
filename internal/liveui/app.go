package liveui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"
	"github.com/seiortech/mtools-plugin-ffmpeg/internal/preview"
	"github.com/seiortech/mtools-plugin-ffmpeg/internal/queue"
	"github.com/seiortech/mtools-sdk/pluginapi"
)

const (
	binaryCacheTTL = 5 * time.Second
	renderMinGap   = 80 * time.Millisecond
)

type cachedBin struct {
	path      string
	err       error
	checkedAt time.Time
}

type State struct {
	ViewportW int
	ViewportH int
	ActiveTab string // "inspector", "files", "queue"
	Mode      string // convert, trim, streams, frames

	// Studio sidebar & sub-tabs
	ConvertSubTab    string // "presets", "format", "quality", "output"
	ConvertMediaKind string // "video", "audio"
	StreamsSubTab    string // "audio", "subtitles", "metadata"
	RailWidth        float32
	RailCollapsed    bool
	ShowCmdPreview   bool
	FullscreenOpen   bool

	// Helpers & settings
	Settings    map[string]string
	HasFFmpeg   bool
	HasFFprobe  bool
	FFmpegPath  string
	FFprobePath string
	OutputDir   string

	// Input files & probe
	InputFiles       []string
	ActiveFileIdx    int
	ProbeLoading     bool
	FilmstripLoading bool
	VideoLoadWaiting bool
	LoadingStage     string
	LoadingFile      string
	LoadingPct       float64
	ProbeError       string
	Probe            *engine.MediaProbe
	StripAsset       string
	PosterAsset      string
	ProxyAsset       string
	ProxyLoading     bool
	ProxyTried       bool
	AssetRev         uint64
	VideoFit         string // "contain" (default) or "cover"

	// Live Video Player & Transport
	PlayheadSec   float64
	IsPlaying     bool
	PlayEnded     bool
	VideoReady    bool
	VideoErr      string
	PlaySeq       uint64
	SeekSeq       uint64
	SeekTargetSec float64
	PlaybackRate  float64
	Volume        float64
	Muted         bool
	LoopSelection bool

	// Trim & Cut List
	StartSec   float64
	EndSec     float64
	StartText  string
	EndText    string
	Clips      []engine.ClipSegment
	NextClipID int

	// Options
	ActivePreset string
	Opts         engine.TranscodeOptions

	// Queue & Status
	QueueItems   []queue.Item
	QueueRunning bool
	StatusMsg    string
	ErrorMsg     string
}

func (s State) IsMediaLoading() bool {
	return s.ProbeLoading || s.FilmstripLoading || s.ProxyLoading || s.VideoLoadWaiting
}

func (s State) EffectiveVideoFit() string {
	if s.VideoFit == "cover" {
		return "cover"
	}
	return "contain"
}

func (s State) buildCurrentJobSpec() (engine.JobSpec, error) {
	if len(s.InputFiles) == 0 {
		return engine.JobSpec{}, engine.ErrNoInputFiles
	}
	idx := s.ActiveFileIdx
	if idx < 0 || idx >= len(s.InputFiles) {
		idx = 0
	}
	inputs := []string{s.InputFiles[idx]}
	if s.Mode == engine.ModeTrim && s.Opts.ConcatMode == "concat_files" && len(s.InputFiles) >= 2 {
		inputs = append([]string(nil), s.InputFiles...)
	}
	return engine.JobSpec{
		Mode:       s.Mode,
		InputPaths: inputs,
		StartSec:   s.StartSec,
		EndSec:     s.EndSec,
		Clips:      append([]engine.ClipSegment(nil), s.Clips...),
		Opts:       s.Opts,
		OutputDir:  s.OutputDir,
	}, nil
}

type App struct {
	dataDir string

	mu             sync.Mutex
	state          State
	session        *pluginapi.UISession
	assetsDir      string
	probeSeq       uint64
	probeCancel    context.CancelFunc
	proxyCancel    context.CancelFunc
	videoWaitTimer *time.Timer
	lastRender     time.Time

	queueMgr *queue.Manager
	ffCache  cachedBin
	fpCache  cachedBin
}

func NewApp(dataDir string) *App {
	a := &App{
		dataDir: dataDir,
		state: State{
			ViewportW:        944,
			ViewportH:        708,
			ActiveTab:        "inspector",
			Mode:             engine.ModeConvert,
			ConvertSubTab:    "presets",
			ConvertMediaKind: "video",
			StreamsSubTab:    "audio",
			RailWidth:        300,
			VideoFit:         "contain",
			PlaybackRate:     1.0,
			Volume:           1.0,
			Opts: engine.TranscodeOptions{
				Container:        "mp4",
				VideoCodec:       "libx264",
				AudioCodec:       "aac",
				QualityMode:      "crf",
				CRF:              23,
				TargetSizeMB:     24,
				VideoBitrateK:    2500,
				AudioBitrateK:    192,
				EncoderPreset:    "medium",
				LosslessCut:      true,
				ConcatMode:       "single",
				AudioTrackIdx:    -1,
				SubMode:          "keep",
				FramesOp:         "gif",
				GIFFPS:           15,
				GIFWidth:         480,
				FrameIntervalSec: 2,
				FrameFormat:      "png",
				SheetCols:        4,
				SheetRows:        4,
			},
		},
	}
	ApplyConvertPreset(&a.state, "universal_mp4")
	a.queueMgr = queue.NewManager(func() {
		a.syncQueueAndRender(false)
	})
	return a
}

func (a *App) Open(ctx context.Context, params pluginapi.UIOpenParams, session *pluginapi.UISession) (pluginapi.UITree, error) {
	a.mu.Lock()
	a.session = session
	if session != nil {
		a.assetsDir = session.AssetsDir()
	}
	if params.Viewport.Width > 0 {
		a.state.ViewportW = params.Viewport.Width
	}
	if params.Viewport.Height > 0 {
		a.state.ViewportH = params.Viewport.Height
	}
	a.applySettingsLocked(params.Settings)
	a.refreshHelpersLocked()
	tree := RenderTree(a.state)
	a.mu.Unlock()
	return tree, nil
}

func (a *App) Close() {
	a.mu.Lock()
	if a.probeCancel != nil {
		a.probeCancel()
		a.probeCancel = nil
	}
	if a.proxyCancel != nil {
		a.proxyCancel()
		a.proxyCancel = nil
	}
	if a.videoWaitTimer != nil {
		a.videoWaitTimer.Stop()
		a.videoWaitTimer = nil
	}
	assetsDir := a.assetsDir
	a.mu.Unlock()

	if a.queueMgr != nil {
		a.queueMgr.CancelActive()
	}
	preview.CleanupAssets(assetsDir, "ff_")
}

func (a *App) InvalidateBinaryCaches() {
	a.mu.Lock()
	a.ffCache = cachedBin{}
	a.fpCache = cachedBin{}
	a.refreshHelpersLocked()
	a.mu.Unlock()
	a.syncQueueAndRender(true)
}

func (a *App) Event(ctx context.Context, ev pluginapi.UIEvent, session *pluginapi.UISession) error {
	a.mu.Lock()
	a.session = session
	if session != nil && a.assetsDir == "" {
		a.assetsDir = session.AssetsDir()
	}
	a.refreshHelpersLocked()
	s := &a.state
	forceRender := true

	if ev.Node == "btn_cancel_load" {
		a.cancelLoadingLocked()
		tree := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(tree, true)
		return nil
	}

	if s.IsMediaLoading() {
		switch ev.Node {
		case "root":
			if ev.Kind == pluginapi.UIEventResize || ev.Kind == pluginapi.UIEventView {
				var w, h int
				if _, err := fmt.Sscanf(ev.Value, "%dx%d", &w, &h); err == nil && w > 0 {
					s.ViewportW, s.ViewportH = w, h
					tree := RenderTree(a.state)
					a.mu.Unlock()
					a.renderTree(tree, true)
					return nil
				}
			}
		case "main_split":
			if ev.Kind == pluginapi.UIEventResize {
				if w, err := strconv.Atoi(ev.Value); err == nil && w >= 240 && w <= 560 {
					s.RailWidth = float32(w)
				}
			}
		case "video_player":
			if ev.Kind == pluginapi.UIEventMedia {
				prevPlaying, prevEnded, prevReady, prevErr := s.IsPlaying, s.PlayEnded, s.VideoReady, s.VideoErr
				applyMediaTelemetry(s, ev.Value)
				if s.VideoReady || s.VideoErr != "" {
					s.VideoLoadWaiting = false
					if a.videoWaitTimer != nil {
						a.videoWaitTimer.Stop()
						a.videoWaitTimer = nil
					}
					a.updateLoadingStageLocked()
				}
				stateChanged := s.IsPlaying != prevPlaying || s.PlayEnded != prevEnded || s.VideoReady != prevReady || s.VideoErr != prevErr
				if s.VideoErr != "" && !s.ProxyTried && s.HasFFmpeg && s.Probe != nil {
					a.startProxyBuildLocked(ctx, a.probeSeq, s.Probe)
					stateChanged = true
				}
				if stateChanged || !s.IsMediaLoading() {
					tree := RenderTree(a.state)
					a.mu.Unlock()
					a.renderTree(tree, true)
					return nil
				}
			}
		}
		a.mu.Unlock()
		return nil
	}

	switch ev.Node {
	case "root":
		switch ev.Kind {
		case pluginapi.UIEventResize, pluginapi.UIEventView:
			var w, h int
			if _, err := fmt.Sscanf(ev.Value, "%dx%d", &w, &h); err == nil && w > 0 {
				s.ViewportW, s.ViewportH = w, h
				forceRender = true
			}
		case pluginapi.UIEventShortcut:
			if a.handleShortcutLocked(ctx, ev.Value) {
				a.mu.Unlock()
				return nil
			}
		}

	case "main_split":
		if ev.Kind == pluginapi.UIEventResize {
			if w, err := strconv.Atoi(ev.Value); err == nil && w >= 240 && w <= 560 {
				s.RailWidth = float32(w)
				forceRender = false
			}
		}
	case "btn_rail_toggle", "btn_rail_expand":
		s.RailCollapsed = !s.RailCollapsed

	case "mode_tabs":
		if ev.Kind == pluginapi.UIEventSelect && ev.Value != "" {
			s.Mode = ev.Value
		}
	case "main_tabs":
		if ev.Kind == pluginapi.UIEventSelect && ev.Value != "" {
			s.ActiveTab = ev.Value
		}
	case "conv_subtabs":
		if ev.Kind == pluginapi.UIEventSelect && ev.Value != "" {
			s.ConvertSubTab = ev.Value
		}
	case "streams_subtabs":
		if ev.Kind == pluginapi.UIEventSelect && ev.Value != "" {
			s.StreamsSubTab = ev.Value
		}

	case "btn_pick_media", "stage_empty":
		paths := parsePickedPaths(ev.Value)
		if len(paths) > 0 {
			s.InputFiles = paths
			s.ActiveFileIdx = 0
			s.ErrorMsg = ""
			a.mu.Unlock()
			a.probeActiveFile(ctx)
			return nil
		}
	case "files_add":
		paths := parsePickedPaths(ev.Value)
		if len(paths) > 0 {
			hadNone := len(s.InputFiles) == 0
			s.InputFiles = appendUniquePaths(s.InputFiles, paths)
			if hadNone {
				s.ActiveFileIdx = 0
				a.mu.Unlock()
				a.probeActiveFile(ctx)
				return nil
			}
		}
	case "btn_clear_files":
		s.InputFiles = nil
		s.ActiveFileIdx = 0
		s.Probe = nil
		s.IsPlaying = false
		s.PlayEnded = false
		s.PlayheadSec = 0
		s.ProxyAsset = ""
		s.PosterAsset = ""
		s.StripAsset = ""

	case "active_file_select":
		if idx, err := strconv.Atoi(ev.Value); err == nil && idx >= 0 && idx < len(s.InputFiles) {
			s.ActiveFileIdx = idx
			a.mu.Unlock()
			a.probeActiveFile(ctx)
			return nil
		}

	// Video Player Telemetry & Transport
	case "video_player":
		if ev.Kind == pluginapi.UIEventMedia {
			prevPlaying, prevEnded, prevReady, prevErr := s.IsPlaying, s.PlayEnded, s.VideoReady, s.VideoErr
			applyMediaTelemetry(s, ev.Value)
			if s.VideoReady || s.VideoErr != "" {
				s.VideoLoadWaiting = false
				if a.videoWaitTimer != nil {
					a.videoWaitTimer.Stop()
					a.videoWaitTimer = nil
				}
				a.updateLoadingStageLocked()
			}
			stateChanged := s.IsPlaying != prevPlaying || s.PlayEnded != prevEnded || s.VideoReady != prevReady || s.VideoErr != prevErr
			if s.VideoErr != "" && !s.ProxyTried && s.HasFFmpeg && s.Probe != nil {
				a.startProxyBuildLocked(ctx, a.probeSeq, s.Probe)
				stateChanged = true
			}
			if !stateChanged {
				a.mu.Unlock()
				return nil
			}
			forceRender = true
		}
	case "btn_video_fit":
		if s.VideoFit == "cover" {
			s.VideoFit = "contain"
		} else {
			s.VideoFit = "cover"
		}
	case "btn_play_pause", "fs_play_pause":
		a.togglePlayPauseLocked()
	case "btn_jump_in":
		target := s.StartSec
		if absFloat(s.PlayheadSec-s.StartSec) < 0.15 {
			target = 0
		}
		a.seekToLocked(target)
	case "btn_jump_out":
		target := s.EndSec
		if s.Probe != nil && absFloat(s.PlayheadSec-s.EndSec) < 0.15 {
			target = s.Probe.DurationSec
		}
		a.seekToLocked(target)
	case "btn_skip_back", "fs_skip_back":
		a.seekToLocked(s.PlayheadSec - 5)
	case "btn_skip_fwd", "fs_skip_fwd":
		a.seekToLocked(s.PlayheadSec + 5)
	case "btn_step_back":
		a.stepFrameLocked(-1)
	case "btn_step_fwd":
		a.stepFrameLocked(+1)
	case "btn_mark_in", "fs_mark_in":
		a.markInAtPlayheadLocked()
	case "btn_mark_out", "fs_mark_out":
		a.markOutAtPlayheadLocked()
	case "btn_loop_toggle", "fs_loop_toggle":
		s.LoopSelection = !s.LoopSelection
	case "btn_speed_cycle":
		s.PlaybackRate = nextPlaybackRate(s.PlaybackRate)
	case "btn_mute_toggle":
		s.Muted = !s.Muted
	case "vol_slider":
		if v, err := strconv.ParseFloat(ev.Value, 64); err == nil {
			s.Volume = minFloat(1, maxFloat(0, v))
			if s.Volume > 0 {
				s.Muted = false
			}
			forceRender = false
		}
	case "btn_snapshot_hd", "fs_snapshot_hd":
		sec := s.PlayheadSec
		a.mu.Unlock()
		a.saveHDFrameAsync(ctx, sec, "frame")
		return nil
	case "btn_fullscreen":
		s.FullscreenOpen = !s.FullscreenOpen
	case "fs_dialog":
		if ev.Kind == pluginapi.UIEventClose {
			s.FullscreenOpen = false
		}

	// Modes
	case "mode_convert":
		s.Mode = engine.ModeConvert
	case "mode_trim":
		s.Mode = engine.ModeTrim
	case "mode_streams":
		s.Mode = engine.ModeStreams
	case "mode_frames":
		s.Mode = engine.ModeFrames

	// Convert controls
	case "conv_media_kind":
		s.ConvertMediaKind = ev.Value
		if ev.Value == "audio" && !engine.IsAudioOnlyContainer(s.Opts.Container) {
			s.Opts.Container = "mp3"
			s.Opts.AudioCodec = "libmp3lame"
		} else if ev.Value == "video" && engine.IsAudioOnlyContainer(s.Opts.Container) {
			s.Opts.Container = "mp4"
			s.Opts.VideoCodec = "libx264"
			s.Opts.AudioCodec = "aac"
		}
	case "conv_container":
		s.Opts.Container = ev.Value
		if engine.IsAudioOnlyContainer(ev.Value) {
			s.ConvertMediaKind = "audio"
		} else {
			s.ConvertMediaKind = "video"
		}
	case "conv_vcodec":
		s.Opts.VideoCodec = ev.Value
	case "conv_acodec":
		s.Opts.AudioCodec = ev.Value
	case "conv_abitrate":
		if v, err := strconv.Atoi(ev.Value); err == nil && v > 0 {
			s.Opts.AudioBitrateK = v
		}
	case "conv_quality_mode":
		s.Opts.QualityMode = ev.Value
	case "conv_crf":
		if v, err := strconv.ParseFloat(ev.Value, 64); err == nil {
			s.Opts.CRF = int(v)
		}
	case "conv_target_mb":
		if v, err := strconv.ParseFloat(ev.Value, 64); err == nil && v > 0 {
			s.Opts.TargetSizeMB = v
		}
	case "conv_vbitrate":
		if v, err := strconv.Atoi(ev.Value); err == nil && v > 0 {
			s.Opts.VideoBitrateK = v
		}
	case "conv_scale":
		if v, err := strconv.Atoi(ev.Value); err == nil && v >= 0 {
			s.Opts.ScaleHeight = v
		}
	case "conv_preset":
		s.Opts.EncoderPreset = ev.Value
	case "btn_toggle_cmd":
		s.ShowCmdPreview = !s.ShowCmdPreview

	// Trim controls
	case "trim_lossless":
		s.Opts.LosslessCut = ev.Value == "true"
	case "trim_concat_mode":
		s.Opts.ConcatMode = ev.Value
	case "trim_canvas":
		if ev.Kind == pluginapi.UIEventClick && s.Probe != nil && s.Probe.DurationSec > 0 {
			var xPx float64
			if _, err := fmt.Sscanf(ev.Value, "%f,", &xPx); err == nil {
				trackW := ComputeTrackWidth(s.ViewportW)
				a.seekToLocked(XToTime(xPx, s.Probe.DurationSec, trackW))
			}
		}
	case "playhead":
		if ev.Kind == pluginapi.UIEventDragEnd && s.Probe != nil && s.Probe.DurationSec > 0 {
			var xPx float64
			_, _ = fmt.Sscanf(ev.Value, "%f,", &xPx)
			trackW := ComputeTrackWidth(s.ViewportW)
			a.seekToLocked(ApplyPlayheadDrag(xPx, s.Probe.DurationSec, trackW))
		}
	case "trim_start", "trim_end":
		if ev.Kind == pluginapi.UIEventDragEnd && s.Probe != nil && s.Probe.DurationSec > 0 {
			var xPx float64
			_, _ = fmt.Sscanf(ev.Value, "%f,", &xPx)
			trackW := ComputeTrackWidth(s.ViewportW)
			ns, ne := ApplyHandleDrag(ev.Node, xPx, s.StartSec, s.EndSec, s.Probe.DurationSec, trackW)
			s.StartSec, s.EndSec = ns, ne
			s.StartText = FormatTimestamp(ns)
			s.EndText = FormatTimestamp(ne)
			if ev.Node == "trim_start" {
				a.seekToLocked(ns)
			} else {
				a.seekToLocked(ne)
			}
		}
	case "trim_window":
		if ev.Kind == pluginapi.UIEventDragEnd && s.Probe != nil && s.Probe.DurationSec > 0 {
			var xPx float64
			_, _ = fmt.Sscanf(ev.Value, "%f,", &xPx)
			trackW := ComputeTrackWidth(s.ViewportW)
			ns, ne := ApplyWindowDrag(xPx-float64(HandleWidth)/2, s.StartSec, s.EndSec, s.Probe.DurationSec, trackW)
			s.StartSec, s.EndSec = ns, ne
			s.StartText = FormatTimestamp(ns)
			s.EndText = FormatTimestamp(ne)
			a.seekToLocked(ns)
		}
	case "time_start_input":
		s.StartText = ev.Value
		if sec, ok := ParseTimestamp(ev.Value); ok && s.Probe != nil {
			if s.Probe.DurationSec > 0 && sec > s.Probe.DurationSec {
				sec = s.Probe.DurationSec
			}
			if sec >= s.EndSec {
				sec = maxFloat(0, s.EndSec-0.5)
			}
			s.StartSec = sec
			a.seekToLocked(sec)
		}
	case "time_end_input":
		s.EndText = ev.Value
		if sec, ok := ParseTimestamp(ev.Value); ok && s.Probe != nil {
			if s.Probe.DurationSec > 0 && sec > s.Probe.DurationSec {
				sec = s.Probe.DurationSec
			}
			if sec <= s.StartSec {
				sec = s.StartSec + 0.5
			}
			s.EndSec = sec
			a.seekToLocked(sec)
		}
	case "btn_start_minus":
		a.nudgeStartLocked(-1)
	case "btn_start_plus":
		a.nudgeStartLocked(+1)
	case "btn_end_minus":
		a.nudgeEndLocked(-1)
	case "btn_end_plus":
		a.nudgeEndLocked(+1)
	case "btn_trim_reset":
		s.StartSec = 0
		if s.Probe != nil && s.Probe.DurationSec > 0 {
			s.EndSec = s.Probe.DurationSec
			s.StartText = FormatTimestamp(0)
			s.EndText = FormatTimestamp(s.Probe.DurationSec)
		} else {
			s.EndSec = 0
			s.StartText = ""
			s.EndText = ""
		}
		a.seekToLocked(0)
	case "btn_add_clip":
		a.addCurrentClipLocked()
	case "btn_split_chapters":
		a.splitChaptersLocked()

	// Streams controls
	case "st_mute_audio":
		s.Opts.MuteAudio = ev.Value == "true"
	case "st_audio_track":
		if idx, err := strconv.Atoi(ev.Value); err == nil {
			s.Opts.AudioTrackIdx = idx
		}
	case "st_ext_audio":
		s.Opts.ExternalAudioPath = firstPickedPath(ev.Value)
	case "st_loudnorm":
		s.Opts.Loudnorm = ev.Value == "true"
	case "st_sub_mode":
		s.Opts.SubMode = ev.Value
	case "st_ext_sub":
		s.Opts.ExternalSubPath = firstPickedPath(ev.Value)
	case "st_strip_meta":
		s.Opts.StripMetadata = ev.Value == "true"
	case "st_meta_title":
		s.Opts.MetaTitle = ev.Value
	case "st_meta_artist":
		s.Opts.MetaArtist = ev.Value

	// Frames controls
	case "fr_op":
		s.Opts.FramesOp = ev.Value
	case "fr_interval":
		if v, err := strconv.ParseFloat(ev.Value, 64); err == nil && v > 0 {
			s.Opts.FrameIntervalSec = v
		}
	case "fr_format":
		s.Opts.FrameFormat = ev.Value
	case "fr_sheet_grid":
		var c, r int
		if _, err := fmt.Sscanf(ev.Value, "%dx%d", &c, &r); err == nil && c >= 2 && r >= 2 {
			s.Opts.SheetCols, s.Opts.SheetRows = c, r
		}
	case "fr_gif_fps":
		if v, err := strconv.Atoi(ev.Value); err == nil && v > 0 {
			s.Opts.GIFFPS = v
		}
	case "fr_gif_width":
		if v, err := strconv.Atoi(ev.Value); err == nil && v > 0 {
			s.Opts.GIFWidth = v
		}

	case "output_dir_picker":
		if strings.TrimSpace(ev.Value) != "" {
			s.OutputDir = ev.Value
		}

	// Queue & Export
	case "btn_export":
		a.mu.Unlock()
		a.enqueueCurrent(ctx, true)
		return nil
	case "btn_add_queue":
		a.mu.Unlock()
		a.enqueueCurrent(ctx, false)
		return nil
	case "btn_start_queue":
		a.mu.Unlock()
		a.startQueue(ctx)
		return nil
	case "btn_cancel_queue":
		a.mu.Unlock()
		a.queueMgr.CancelActive()
		return nil
	case "btn_retry_failed":
		n := a.queueMgr.RetryFailed()
		if n > 0 {
			s.StatusMsg = fmt.Sprintf("Re-queued %d failed item(s)", n)
			a.mu.Unlock()
			a.startQueue(ctx)
			return nil
		}
	case "btn_clear_done":
		a.mu.Unlock()
		a.queueMgr.ClearCompleted()
		return nil

	default:
		if switchFile := a.handleDynamicNodeLocked(ev); switchFile {
			a.mu.Unlock()
			a.probeActiveFile(ctx)
			return nil
		}
	}

	tree := RenderTree(a.state)
	a.mu.Unlock()
	a.renderTree(tree, forceRender)
	return nil
}

func (a *App) handleShortcutLocked(ctx context.Context, spec string) bool {
	s := &a.state
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "escape":
		s.FullscreenOpen = false
	case "space", "k":
		a.togglePlayPauseLocked()
	case "j":
		a.seekToLocked(s.PlayheadSec - 5)
	case "l":
		a.seekToLocked(s.PlayheadSec + 5)
	case "i":
		a.markInAtPlayheadLocked()
	case "o":
		a.markOutAtPlayheadLocked()
	case "home":
		a.seekToLocked(s.StartSec)
	case "end":
		a.seekToLocked(s.EndSec)
	case ",":
		a.stepFrameLocked(-1)
	case ".":
		a.stepFrameLocked(+1)
	case "m":
		s.Muted = !s.Muted
	case "f":
		if s.Probe != nil && s.Probe.HasVideo() {
			s.FullscreenOpen = !s.FullscreenOpen
		}
	case "shift+l":
		s.LoopSelection = !s.LoopSelection
	case "s":
		if s.Probe != nil && s.Probe.HasVideo() && s.HasFFmpeg {
			sec := s.PlayheadSec
			a.mu.Unlock()
			a.saveHDFrameAsync(ctx, sec, "frame")
			a.mu.Lock()
			return true
		}
	}
	return false
}

func (a *App) handleDynamicNodeLocked(ev pluginapi.UIEvent) bool {
	s := &a.state
	switch {
	case strings.HasPrefix(ev.Node, "preset_"):
		ApplyConvertPreset(s, strings.TrimPrefix(ev.Node, "preset_"))
	case strings.HasPrefix(ev.Node, "file_select_"):
		if idx, err := strconv.Atoi(strings.TrimPrefix(ev.Node, "file_select_")); err == nil && idx >= 0 && idx < len(s.InputFiles) {
			s.ActiveFileIdx = idx
			return true
		}
	case strings.HasPrefix(ev.Node, "file_remove_"):
		if idx, err := strconv.Atoi(strings.TrimPrefix(ev.Node, "file_remove_")); err == nil && idx >= 0 && idx < len(s.InputFiles) {
			wasActive := idx == s.ActiveFileIdx
			s.InputFiles = append(s.InputFiles[:idx], s.InputFiles[idx+1:]...)
			if len(s.InputFiles) == 0 {
				s.ActiveFileIdx = 0
				s.Probe = nil
				s.IsPlaying = false
				s.ProxyAsset = ""
				s.PosterAsset = ""
				s.StripAsset = ""
				return false
			}
			if s.ActiveFileIdx >= len(s.InputFiles) {
				s.ActiveFileIdx = len(s.InputFiles) - 1
			}
			return wasActive
		}
	case strings.HasPrefix(ev.Node, "clip_select_"):
		id := strings.TrimPrefix(ev.Node, "clip_select_")
		for _, cl := range s.Clips {
			if cl.ID == id {
				s.StartSec = cl.StartSec
				s.EndSec = cl.EndSec
				s.StartText = FormatTimestamp(cl.StartSec)
				s.EndText = FormatTimestamp(cl.EndSec)
				a.seekToLocked(cl.StartSec)
				break
			}
		}
	case strings.HasPrefix(ev.Node, "clip_remove_"):
		id := strings.TrimPrefix(ev.Node, "clip_remove_")
		filtered := s.Clips[:0]
		for _, cl := range s.Clips {
			if cl.ID != id {
				filtered = append(filtered, cl)
			}
		}
		s.Clips = filtered
	case strings.HasPrefix(ev.Node, "q_remove_"):
		id := strings.TrimPrefix(ev.Node, "q_remove_")
		a.queueMgr.Remove(id)
	}
	return false
}

func (a *App) togglePlayPauseLocked() {
	s := &a.state
	if s.Probe == nil {
		return
	}
	if s.PlayEnded && !s.IsPlaying {
		target := s.StartSec
		if target < 0 || target >= s.Probe.DurationSec {
			target = 0
		}
		a.seekToLocked(target)
		s.PlayEnded = false
		s.IsPlaying = true
		s.PlaySeq++
		return
	}
	s.IsPlaying = !s.IsPlaying
	s.PlaySeq++
}

func (a *App) seekToLocked(sec float64) {
	s := &a.state
	if s.Probe == nil {
		return
	}
	dur := s.Probe.DurationSec
	if sec < 0 {
		sec = 0
	}
	if dur > 0 && sec > dur {
		sec = dur
	}
	s.PlayheadSec = sec
	s.SeekTargetSec = sec
	s.SeekSeq++
	if dur <= 0 || sec < dur-0.05 {
		s.PlayEnded = false
	}
}

func (a *App) stepFrameLocked(dir int) {
	s := &a.state
	if s.Probe == nil {
		return
	}
	if s.IsPlaying {
		s.IsPlaying = false
		s.PlaySeq++
	}
	step := 0.04 // 25fps fallback
	if s.Probe.HasVideo() && s.Probe.VideoStreams[0].FPS > 0 {
		step = 1.0 / s.Probe.VideoStreams[0].FPS
	}
	a.seekToLocked(s.PlayheadSec + float64(dir)*step)
}

func (a *App) markInAtPlayheadLocked() {
	s := &a.state
	if s.Probe == nil || s.Probe.DurationSec <= 0 {
		return
	}
	ns := maxFloat(0, minFloat(s.PlayheadSec, s.EndSec-0.5))
	s.StartSec = ns
	s.StartText = FormatTimestamp(ns)
}

func (a *App) markOutAtPlayheadLocked() {
	s := &a.state
	if s.Probe == nil || s.Probe.DurationSec <= 0 {
		return
	}
	ne := minFloat(s.Probe.DurationSec, maxFloat(s.PlayheadSec, s.StartSec+0.5))
	s.EndSec = ne
	s.EndText = FormatTimestamp(ne)
}

func (a *App) nudgeStartLocked(delta float64) {
	s := &a.state
	if s.Probe == nil || s.Probe.DurationSec <= 0 {
		return
	}
	ns := s.StartSec + delta
	if ns < 0 {
		ns = 0
	}
	if ns > s.EndSec-0.5 {
		ns = maxFloat(0, s.EndSec-0.5)
	}
	s.StartSec = ns
	s.StartText = FormatTimestamp(ns)
	a.seekToLocked(ns)
}

func (a *App) nudgeEndLocked(delta float64) {
	s := &a.state
	if s.Probe == nil || s.Probe.DurationSec <= 0 {
		return
	}
	ne := s.EndSec + delta
	if ne > s.Probe.DurationSec {
		ne = s.Probe.DurationSec
	}
	if ne < s.StartSec+0.5 {
		ne = minFloat(s.Probe.DurationSec, s.StartSec+0.5)
	}
	s.EndSec = ne
	s.EndText = FormatTimestamp(ne)
	a.seekToLocked(ne)
}

func (a *App) applySettingsLocked(settings map[string]string) {
	a.state.Settings = settings
	outDir := expandHome(settings["output_dir"])
	if outDir == "" {
		outDir = expandHome("~/Downloads")
	}
	a.state.OutputDir = outDir
	if c := strings.TrimSpace(settings["default_container"]); c != "" {
		a.state.Opts.Container = c
		if engine.IsAudioOnlyContainer(c) {
			a.state.ConvertMediaKind = "audio"
		}
	}
	a.state.Opts.Overwrite = settings["overwrite_policy"] == "overwrite"
}

func (a *App) refreshHelpersLocked() {
	now := time.Now()
	if a.ffCache.checkedAt.IsZero() || now.Sub(a.ffCache.checkedAt) > binaryCacheTTL {
		p, err := engine.ResolveFFmpeg(a.state.Settings["ffmpeg_path"], a.dataDir, a.state.Settings)
		a.ffCache = cachedBin{path: p, err: err, checkedAt: now}
	}
	if a.fpCache.checkedAt.IsZero() || now.Sub(a.fpCache.checkedAt) > binaryCacheTTL {
		p, err := engine.ResolveFFprobe(a.state.Settings["ffprobe_path"], a.dataDir, a.state.Settings)
		a.fpCache = cachedBin{path: p, err: err, checkedAt: now}
	}
	a.state.HasFFmpeg = a.ffCache.err == nil
	a.state.FFmpegPath = a.ffCache.path
	a.state.HasFFprobe = a.fpCache.err == nil
	a.state.FFprobePath = a.fpCache.path
}

func (a *App) updateLoadingStageLocked() {
	s := &a.state
	switch {
	case s.ProbeLoading:
		s.LoadingStage = "Inspecting media streams…"
		s.LoadingPct = 15
	case s.ProxyLoading && s.FilmstripLoading:
		s.LoadingStage = "Preparing playback proxy & timeline filmstrip…"
		s.LoadingPct = 35
	case s.ProxyLoading:
		s.LoadingStage = "Preparing smooth playback proxy…"
		s.LoadingPct = 55
	case s.FilmstripLoading:
		s.LoadingStage = "Building timeline filmstrip…"
		s.LoadingPct = 55
	case s.VideoLoadWaiting:
		s.LoadingStage = "Loading video player…"
		s.LoadingPct = 85
	default:
		s.LoadingStage = ""
		s.LoadingPct = 100
	}
}

func (a *App) armVideoReadyFallbackLocked(seq uint64) {
	if a.videoWaitTimer != nil {
		a.videoWaitTimer.Stop()
	}
	a.videoWaitTimer = time.AfterFunc(6*time.Second, func() {
		a.mu.Lock()
		if seq != a.probeSeq || !a.state.VideoLoadWaiting {
			a.mu.Unlock()
			return
		}
		a.state.VideoLoadWaiting = false
		a.updateLoadingStageLocked()
		t := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(t, true)
	})
}

func (a *App) cancelLoadingLocked() {
	if a.probeCancel != nil {
		a.probeCancel()
		a.probeCancel = nil
	}
	if a.proxyCancel != nil {
		a.proxyCancel()
		a.proxyCancel = nil
	}
	if a.videoWaitTimer != nil {
		a.videoWaitTimer.Stop()
		a.videoWaitTimer = nil
	}
	a.probeSeq++
	s := &a.state
	s.ProbeLoading = false
	s.FilmstripLoading = false
	s.ProxyLoading = false
	s.VideoLoadWaiting = false
	s.LoadingStage = ""
	s.LoadingFile = ""
	s.LoadingPct = 0
	s.IsPlaying = false
	s.PlayEnded = false
	s.PlayheadSec = 0
	s.VideoReady = false
	s.VideoErr = ""
	s.Probe = nil
	s.StripAsset = ""
	s.PosterAsset = ""
	s.ProxyAsset = ""
	s.StatusMsg = "Media loading canceled."
}

func (a *App) probeActiveFile(parentCtx context.Context) {
	a.mu.Lock()
	if len(a.state.InputFiles) == 0 || a.state.ActiveFileIdx >= len(a.state.InputFiles) {
		a.mu.Unlock()
		return
	}
	if !a.state.HasFFmpeg {
		a.state.ProbeError = "FFmpeg is required to inspect media files. Click Download FFmpeg above."
		tree := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(tree, true)
		return
	}
	if a.probeCancel != nil {
		a.probeCancel()
	}
	if a.proxyCancel != nil {
		a.proxyCancel()
		a.proxyCancel = nil
	}
	if a.videoWaitTimer != nil {
		a.videoWaitTimer.Stop()
		a.videoWaitTimer = nil
	}
	pctx, cancel := context.WithCancel(parentCtx)
	a.probeCancel = cancel
	a.probeSeq++
	seq := a.probeSeq

	filePath := a.state.InputFiles[a.state.ActiveFileIdx]
	ffBin := a.state.FFmpegPath
	fpBin := a.state.FFprobePath
	assetsDir := a.assetsDir
	a.state.ProbeLoading = true
	a.state.FilmstripLoading = false
	a.state.VideoLoadWaiting = false
	a.state.ProbeError = ""
	a.state.ErrorMsg = ""
	a.state.IsPlaying = false
	a.state.PlayEnded = false
	a.state.PlayheadSec = 0
	a.state.SeekTargetSec = 0
	a.state.PlaySeq++
	a.state.SeekSeq++
	a.state.VideoReady = false
	a.state.VideoErr = ""
	a.state.ProxyAsset = ""
	a.state.StripAsset = ""
	a.state.PosterAsset = ""
	a.state.ProxyLoading = false
	a.state.ProxyTried = false
	a.state.FullscreenOpen = false
	a.state.LoadingFile = filepath.Base(filePath)
	a.updateLoadingStageLocked()
	a.state.StatusMsg = ""
	tree := RenderTree(a.state)
	a.mu.Unlock()
	a.renderTree(tree, true)

	go func() {
		defer cancel()
		probe, err := engine.ProbeMedia(pctx, fpBin, ffBin, filePath)
		if pctx.Err() != nil {
			return
		}
		if err != nil {
			a.mu.Lock()
			if seq == a.probeSeq {
				a.state.ProbeLoading = false
				a.state.FilmstripLoading = false
				a.state.ProxyLoading = false
				a.state.VideoLoadWaiting = false
				a.updateLoadingStageLocked()
				a.state.ProbeError = err.Error()
				a.state.StatusMsg = ""
				t := RenderTree(a.state)
				a.mu.Unlock()
				a.renderTree(t, true)
				return
			}
			a.mu.Unlock()
			return
		}

		preview.CleanupAssets(assetsDir, "ff_")
		a.mu.Lock()
		if seq != a.probeSeq {
			a.mu.Unlock()
			return
		}
		a.state.AssetRev++
		rev := a.state.AssetRev
		a.state.ProbeLoading = false
		a.state.Probe = probe
		a.state.StartSec = 0
		a.state.EndSec = probe.DurationSec
		a.state.StartText = FormatTimestamp(0)
		a.state.EndText = FormatTimestamp(probe.DurationSec)
		a.state.Clips = nil
		a.state.StatusMsg = ""

		needStrip := assetsDir != "" && probe.HasVideo() && probe.DurationSec > 0
		needProxy := assetsDir != "" && preview.NeedsPreviewProxy(probe)
		needPlayerWait := probe.HasVideo()
		a.state.FilmstripLoading = needStrip
		a.state.ProxyLoading = needProxy
		a.state.ProxyTried = needProxy
		a.state.VideoLoadWaiting = needPlayerWait
		a.updateLoadingStageLocked()
		if needPlayerWait && !needProxy {
			a.armVideoReadyFallbackLocked(seq)
		}
		initialTree := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(initialTree, true)

		var wg sync.WaitGroup
		if needStrip {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sName := fmt.Sprintf("ff_strip_%d.jpg", rev)
				var stripAsset string
				if preview.BuildFilmstrip(pctx, ffBin, filePath, probe.DurationSec, 8, 96, 54, filepath.Join(assetsDir, sName)) == nil {
					stripAsset = sName
				}
				if pctx.Err() != nil {
					return
				}
				a.mu.Lock()
				if seq != a.probeSeq {
					a.mu.Unlock()
					return
				}
				a.state.StripAsset = stripAsset
				a.state.FilmstripLoading = false
				a.updateLoadingStageLocked()
				t := RenderTree(a.state)
				a.mu.Unlock()
				a.renderTree(t, true)
			}()
		}

		if needProxy {
			wg.Add(1)
			go func() {
				defer wg.Done()
				proxyName := fmt.Sprintf("ff_proxy_%d.mp4", rev)
				perr := preview.BuildPlayableProxy(pctx, ffBin, filePath, probe, filepath.Join(assetsDir, proxyName))
				if pctx.Err() != nil {
					return
				}
				a.mu.Lock()
				if seq != a.probeSeq {
					a.mu.Unlock()
					return
				}
				a.state.ProxyLoading = false
				if perr == nil {
					a.state.ProxyAsset = proxyName
					a.state.VideoErr = ""
					if a.state.VideoLoadWaiting {
						a.armVideoReadyFallbackLocked(seq)
					}
				} else {
					a.state.VideoLoadWaiting = false
					a.state.VideoErr = "Preview proxy failed: " + perr.Error()
				}
				a.updateLoadingStageLocked()
				t := RenderTree(a.state)
				a.mu.Unlock()
				a.renderTree(t, true)
			}()
		}

		wg.Wait()
	}()
}

func (a *App) startProxyBuildLocked(parentCtx context.Context, seq uint64, probe *engine.MediaProbe) {
	if a.assetsDir == "" || !a.state.HasFFmpeg || probe == nil || a.state.ProxyLoading {
		return
	}
	if a.proxyCancel != nil {
		a.proxyCancel()
	}
	xctx, cancel := context.WithCancel(parentCtx)
	a.proxyCancel = cancel
	a.state.AssetRev++
	rev := a.state.AssetRev
	a.state.ProxyLoading = true
	a.state.ProxyTried = true
	a.state.VideoLoadWaiting = probe.HasVideo()
	a.updateLoadingStageLocked()
	ffBin := a.state.FFmpegPath
	assetsDir := a.assetsDir
	filePath := probe.Path

	go func() {
		defer cancel()
		proxyName := fmt.Sprintf("ff_proxy_%d.mp4", rev)
		err := preview.BuildPlayableProxy(xctx, ffBin, filePath, probe, filepath.Join(assetsDir, proxyName))
		if xctx.Err() != nil {
			return
		}
		a.mu.Lock()
		if seq != a.probeSeq {
			a.mu.Unlock()
			return
		}
		a.state.ProxyLoading = false
		if err == nil {
			a.state.ProxyAsset = proxyName
			a.state.VideoErr = ""
			if a.state.VideoLoadWaiting {
				a.armVideoReadyFallbackLocked(seq)
			}
		} else {
			a.state.VideoLoadWaiting = false
		}
		a.updateLoadingStageLocked()
		t := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(t, true)
	}()
}

func (a *App) addCurrentClipLocked() {
	s := &a.state
	if s.Probe == nil || s.EndSec <= s.StartSec {
		return
	}
	s.NextClipID++
	s.Clips = append(s.Clips, engine.ClipSegment{
		ID:       fmt.Sprintf("c%d", s.NextClipID),
		Title:    fmt.Sprintf("Clip %d", s.NextClipID),
		StartSec: s.StartSec,
		EndSec:   s.EndSec,
	})
	s.StatusMsg = fmt.Sprintf("Added Clip %d (%s–%s)", s.NextClipID, FormatTimestamp(s.StartSec), FormatTimestamp(s.EndSec))
}

func (a *App) splitChaptersLocked() {
	s := &a.state
	if s.Probe == nil || len(s.Probe.Chapters) == 0 {
		return
	}
	s.Clips = nil
	for i, ch := range s.Probe.Chapters {
		s.NextClipID++
		end := ch.EndSec
		if end <= ch.StartSec {
			end = s.Probe.DurationSec
		}
		s.Clips = append(s.Clips, engine.ClipSegment{
			ID:       fmt.Sprintf("ch%d_%d", i+1, s.NextClipID),
			Title:    ch.Title,
			StartSec: ch.StartSec,
			EndSec:   end,
		})
	}
	s.StatusMsg = fmt.Sprintf("Split %d chapters into cut list", len(s.Clips))
}

func (a *App) saveHDFrameAsync(ctx context.Context, sec float64, tag string) {
	a.mu.Lock()
	if !a.state.HasFFmpeg || a.state.Probe == nil {
		a.state.ErrorMsg = "FFmpeg and a loaded video are required for HD snapshots."
		t := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(t, true)
		return
	}
	ffBin := a.state.FFmpegPath
	filePath := a.state.Probe.Path
	outDir := a.state.OutputDir
	if outDir == "" {
		outDir = expandHome("~/Downloads")
	}
	a.state.StatusMsg = "Capturing HD frame..."
	t := RenderTree(a.state)
	a.mu.Unlock()
	a.renderTree(t, true)

	go func() {
		name := fmt.Sprintf("frame_%s_%ds.png", tag, int(sec))
		outPath := filepath.Join(outDir, name)
		err := preview.SaveHDFrame(ctx, ffBin, filePath, sec, outPath)
		a.mu.Lock()
		if err != nil {
			a.state.ErrorMsg = "HD frame capture failed: " + err.Error()
			a.state.StatusMsg = ""
		} else {
			a.state.StatusMsg = "Saved HD snapshot: " + name
		}
		tree := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(tree, true)
	}()
}

func (a *App) enqueueCurrent(ctx context.Context, startNow bool) {
	a.mu.Lock()
	spec, err := a.state.buildCurrentJobSpec()
	if err != nil {
		a.state.ErrorMsg = err.Error()
		t := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(t, true)
		return
	}
	plan, err := engine.BuildCommandPlan(spec, a.state.Probe)
	if err != nil {
		a.state.ErrorMsg = err.Error()
		t := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(t, true)
		return
	}
	title := filepath.Base(spec.InputPaths[0])
	label := strings.ToUpper(a.state.Mode) + " · " + plan.Summary
	a.state.ErrorMsg = ""
	a.state.ActiveTab = "queue"
	a.state.RailCollapsed = false
	a.state.StatusMsg = "Queued: " + title
	a.mu.Unlock()

	a.queueMgr.Add(title, label, spec, plan)
	if startNow {
		a.startQueue(ctx)
	}
}

func (a *App) startQueue(ctx context.Context) {
	a.mu.Lock()
	ffBin := a.state.FFmpegPath
	overwrite := a.state.Opts.Overwrite
	a.mu.Unlock()

	a.queueMgr.Start(ctx, 1, func(jobCtx context.Context, item queue.Item, onProg func(pluginapi.Progress)) (pluginapi.RunResult, error) {
		job := engine.Job{
			FFmpegPath: ffBin,
			Plan:       item.Plan,
			Overwrite:  overwrite,
		}
		return job.Run(jobCtx, func(p pluginapi.Progress) {
			p.JobID = item.ID
			onProg(p)
		})
	})
}

func (a *App) syncQueueAndRender(force bool) {
	items, running := a.queueMgr.Snapshot()
	a.mu.Lock()
	a.state.QueueItems = items
	a.state.QueueRunning = running
	tree := RenderTree(a.state)
	a.mu.Unlock()
	a.renderTree(tree, force)
}

func (a *App) renderTree(tree pluginapi.UITree, force bool) {
	a.mu.Lock()
	sess := a.session
	if sess == nil {
		a.mu.Unlock()
		return
	}
	now := time.Now()
	if !force && now.Sub(a.lastRender) < renderMinGap {
		a.mu.Unlock()
		return
	}
	a.lastRender = now
	a.mu.Unlock()
	_ = sess.Render(tree)
}

func applyMediaTelemetry(s *State, raw string) {
	for _, part := range strings.Split(raw, "|") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch k {
		case "pos":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
				s.PlayheadSec = f
			}
		case "playing":
			s.IsPlaying = v == "1"
		case "ended":
			s.PlayEnded = v == "1"
		case "ready":
			s.VideoReady = v == "1"
		case "err":
			s.VideoErr = v
		}
	}
}

func nextPlaybackRate(cur float64) float64 {
	switch {
	case cur < 0.75:
		return 1.0
	case cur < 1.25:
		return 1.5
	case cur < 1.75:
		return 2.0
	default:
		return 0.5
	}
}

func parsePickedPaths(raw string) []string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "[") {
		var list []string
		if err := json.Unmarshal([]byte(s), &list); err == nil {
			var out []string
			for _, p := range list {
				if tp := strings.TrimSpace(p); tp != "" {
					out = append(out, tp)
				}
			}
			return out
		}
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if tp := strings.TrimSpace(line); tp != "" {
			out = append(out, tp)
		}
	}
	return out
}

func firstPickedPath(raw string) string {
	list := parsePickedPaths(raw)
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

func appendUniquePaths(existing, added []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, p := range existing {
		seen[p] = true
	}
	for _, p := range added {
		if !seen[p] {
			seen[p] = true
			existing = append(existing, p)
		}
	}
	return existing
}

func expandHome(p string) string {
	s := strings.TrimSpace(p)
	if s == "~" || strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if s == "~" {
				return home
			}
			return filepath.Join(home, s[2:])
		}
	}
	return s
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func absFloat(a float64) float64 {
	if a < 0 {
		return -a
	}
	return a
}
