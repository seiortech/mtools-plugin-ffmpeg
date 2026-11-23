package liveui

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
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

	VideoPreviewTrimOnly = "trim_only"
	VideoPreviewAuto     = "auto"
	VideoPreviewManual   = "manual"
	VideoPreviewOff      = "off"

	ProxyPolicyRemuxOnly = "remux_only"
	ProxyPolicyAuto      = "auto"
	ProxyPolicyManual    = "manual"
	ProxyPolicyOff       = "off"
)

func normalizeVideoPreviewMode(v string) string {
	switch strings.TrimSpace(v) {
	case VideoPreviewTrimOnly, VideoPreviewManual, VideoPreviewOff:
		return strings.TrimSpace(v)
	default:
		return VideoPreviewAuto
	}
}

func normalizeProxyPolicy(v string) string {
	switch strings.TrimSpace(v) {
	case ProxyPolicyRemuxOnly, ProxyPolicyManual, ProxyPolicyOff:
		return strings.TrimSpace(v)
	default:
		return ProxyPolicyAuto
	}
}

func shouldAutoPreview(videoPreviewMode, mode string) bool {
	switch normalizeVideoPreviewMode(videoPreviewMode) {
	case VideoPreviewTrimOnly:
		return modeUsesTrimRange(mode)
	case VideoPreviewManual, VideoPreviewOff:
		return false
	default:
		return true
	}
}

type cachedBin struct {
	path      string
	err       error
	checkedAt time.Time
}

type cachedHWCaps struct {
	ffPath  string
	caps    engine.HWCapabilities
	checked bool
}

type ModeSession struct {
	InputFiles        []string
	ActiveFileIdx     int
	ProbeLoading      bool
	FilmstripLoading  bool
	PreviewLoaded     bool
	ProxyRequired     bool
	ProxyReason       string
	ProxyStage        string
	TryNativeOverride bool
	LoadingStage      string
	LoadingFile       string
	LoadingPct        float64
	ProbeError        string
	Probe             *engine.MediaProbe
	StripAsset        string
	PosterAsset       string
	ProxyAsset        string
	ProxyLoading      bool
	ProxyTried        bool
	VideoFit          string // "contain" (default) or "cover"

	PlayheadSec   float64
	IsPlaying     bool
	PlayEnded     bool
	VideoReady    bool
	VideoErr      string
	SeekTargetSec float64
	PlaybackRate  float64
	Volume        float64
	Muted         bool
	LoopSelection bool

	StartSec   float64
	EndSec     float64
	StartText  string
	EndText    string
	Clips      []engine.ClipSegment
	NextClipID int
}

func defaultModeSession() ModeSession {
	return ModeSession{
		VideoFit:     "contain",
		PlaybackRate: 1.0,
		Volume:       1.0,
	}
}

func (ms ModeSession) IsMediaLoading() bool {
	return ms.ProbeLoading
}

func (ms ModeSession) EffectiveVideoFit() string {
	if ms.VideoFit == "cover" {
		return "cover"
	}
	return "contain"
}

type ModeSessions struct {
	Convert ModeSession
	Trim    ModeSession
	Streams ModeSession
	Frames  ModeSession
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
	DeckCollapsed    bool
	ShowCmdPreview   bool
	FullscreenOpen   bool

	// Helpers & settings
	Settings           map[string]string
	HasFFmpeg          bool
	HasFFprobe         bool
	FFmpegPath         string
	FFprobePath        string
	OutputDir          string
	VideoPreviewMode   string
	TimelineFilmstrip  bool
	PreviewProxyPolicy string
	HWAccelMode        string
	HWBackend          string
	HWCaps             engine.HWCapabilities
	HWPlan             engine.HWPlan

	// Workspace-wide monotonic media asset & player sequences
	AssetRev uint64
	PlaySeq  uint64
	SeekSeq  uint64

	// Per-mode separated media, player, and trim sessions
	Sessions ModeSessions

	// Options
	ActivePreset string
	Opts         engine.TranscodeOptions

	// Shared Queue & Status
	QueueItems   []queue.Item
	QueueRunning bool
	StatusMsg    string
	ErrorMsg     string
}

func normalizeMode(mode string) string {
	switch mode {
	case engine.ModeTrim, engine.ModeStreams, engine.ModeFrames:
		return mode
	default:
		return engine.ModeConvert
	}
}

func modeUsesTrimRange(mode string) bool {
	return mode == engine.ModeTrim || mode == engine.ModeFrames
}

func modeUsesClips(mode string) bool {
	return mode == engine.ModeTrim
}

func (s *State) SessionFor(mode string) *ModeSession {
	switch normalizeMode(mode) {
	case engine.ModeTrim:
		return &s.Sessions.Trim
	case engine.ModeStreams:
		return &s.Sessions.Streams
	case engine.ModeFrames:
		return &s.Sessions.Frames
	default:
		return &s.Sessions.Convert
	}
}

func (s *State) ActiveSession() *ModeSession {
	return s.SessionFor(s.Mode)
}

func (s State) CurrentSession() ModeSession {
	switch normalizeMode(s.Mode) {
	case engine.ModeTrim:
		return s.Sessions.Trim
	case engine.ModeStreams:
		return s.Sessions.Streams
	case engine.ModeFrames:
		return s.Sessions.Frames
	default:
		return s.Sessions.Convert
	}
}

func (s State) IsMediaLoading() bool {
	return s.CurrentSession().IsMediaLoading()
}

func (s State) EffectiveVideoFit() string {
	return s.CurrentSession().EffectiveVideoFit()
}

func (s State) buildCurrentJobSpec() (engine.JobSpec, error) {
	ms := s.CurrentSession()
	if len(ms.InputFiles) == 0 {
		return engine.JobSpec{}, engine.ErrNoInputFiles
	}
	idx := ms.ActiveFileIdx
	if idx < 0 || idx >= len(ms.InputFiles) {
		idx = 0
	}
	inputs := []string{ms.InputFiles[idx]}
	if s.Mode == engine.ModeTrim && s.Opts.ConcatMode == "concat_files" && len(ms.InputFiles) >= 2 {
		inputs = append([]string(nil), ms.InputFiles...)
	}
	var startSec, endSec float64
	var clips []engine.ClipSegment
	if modeUsesTrimRange(s.Mode) {
		startSec = ms.StartSec
		endSec = ms.EndSec
	}
	if modeUsesClips(s.Mode) {
		clips = append([]engine.ClipSegment(nil), ms.Clips...)
	}
	return engine.JobSpec{
		Mode:       s.Mode,
		InputPaths: inputs,
		StartSec:   startSec,
		EndSec:     endSec,
		Clips:      clips,
		Opts:       s.Opts,
		OutputDir:  s.OutputDir,
		HW:         s.HWPlan,
	}, nil
}

type modeLoadCtrl struct {
	probeSeq    uint64
	probeCancel context.CancelFunc
	stripCancel context.CancelFunc
	proxyCancel context.CancelFunc
}

type App struct {
	dataDir string

	mu              sync.Mutex
	state           State
	session         *pluginapi.UISession
	assetsDir       string
	settingsFile    string
	settingsModTime time.Time
	modeWorkers     map[string]*modeLoadCtrl
	lastRender      time.Time

	queueMgr *queue.Manager
	ffCache  cachedBin
	fpCache  cachedBin
	hwCache  cachedHWCaps
}

func NewApp(dataDir string) *App {
	a := &App{
		dataDir: dataDir,
		modeWorkers: map[string]*modeLoadCtrl{
			engine.ModeConvert: {},
			engine.ModeTrim:    {},
			engine.ModeStreams: {},
			engine.ModeFrames:  {},
		},
		state: State{
			ViewportW:          944,
			ViewportH:          708,
			ActiveTab:          "inspector",
			Mode:               engine.ModeConvert,
			ConvertSubTab:      "presets",
			ConvertMediaKind:   "video",
			StreamsSubTab:      "audio",
			RailWidth:          300,
			VideoPreviewMode:   VideoPreviewAuto,
			TimelineFilmstrip:  true,
			PreviewProxyPolicy: ProxyPolicyAuto,
			HWAccelMode:        engine.HWAccelAuto,
			HWBackend:          engine.HWBackendAuto,
			Sessions: ModeSessions{
				Convert: defaultModeSession(),
				Trim:    defaultModeSession(),
				Streams: defaultModeSession(),
				Frames:  defaultModeSession(),
			},
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
	if pDir := strings.TrimSpace(params.Settings[pluginapi.SettingPluginDataDir]); pDir != "" {
		a.settingsFile = filepath.Join(pDir, "settings.json")
	} else if a.dataDir != "" {
		a.settingsFile = filepath.Join(a.dataDir, "settings.json")
	}
	if a.settingsFile != "" {
		if info, err := os.Stat(a.settingsFile); err == nil {
			a.settingsModTime = info.ModTime()
		}
	}
	a.applySettingsLocked(params.Settings)
	a.refreshHelpersLocked()
	tree := RenderTree(a.state)
	a.mu.Unlock()
	return tree, nil
}

func (a *App) workerCtrlLocked(mode string) *modeLoadCtrl {
	m := normalizeMode(mode)
	if a.modeWorkers == nil {
		a.modeWorkers = make(map[string]*modeLoadCtrl)
	}
	ctrl := a.modeWorkers[m]
	if ctrl == nil {
		ctrl = &modeLoadCtrl{}
		a.modeWorkers[m] = ctrl
	}
	return ctrl
}

func (a *App) Close() {
	a.mu.Lock()
	for _, ctrl := range a.modeWorkers {
		if ctrl == nil {
			continue
		}
		if ctrl.probeCancel != nil {
			ctrl.probeCancel()
			ctrl.probeCancel = nil
		}
		if ctrl.stripCancel != nil {
			ctrl.stripCancel()
			ctrl.stripCancel = nil
		}
		if ctrl.proxyCancel != nil {
			ctrl.proxyCancel()
			ctrl.proxyCancel = nil
		}
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
	a.hwCache = cachedHWCaps{}
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
	if ev.Node == "settings" && ev.Kind == pluginapi.UIEventChange {
		a.reloadSettingsFromDiskLocked(true)
		a.refreshHelpersLocked()
		a.reconcileSessionsWithSettingsLocked(ctx)
		tree := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(tree, true)
		return nil
	}
	a.reloadSettingsFromDiskLocked(false)
	a.refreshHelpersLocked()
	s := &a.state
	activeMode := normalizeMode(s.Mode)
	ms := s.SessionFor(activeMode)
	ctrl := a.workerCtrlLocked(activeMode)
	forceRender := true

	if ev.Node == "btn_cancel_load" {
		a.cancelLoadingLocked(activeMode)
		tree := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(tree, true)
		return nil
	}

	if ms.IsMediaLoading() {
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
	case "btn_deck_toggle", "btn_top_deck_toggle":
		s.DeckCollapsed = !s.DeckCollapsed

	case "mode_tabs":
		if ev.Kind == pluginapi.UIEventSelect && ev.Value != "" {
			a.switchModeLocked(ev.Value)
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
			ms.InputFiles = paths
			ms.ActiveFileIdx = 0
			s.ErrorMsg = ""
			a.mu.Unlock()
			a.probeActiveFile(ctx, activeMode)
			return nil
		}
	case "files_add":
		paths := parsePickedPaths(ev.Value)
		if len(paths) > 0 {
			hadNone := len(ms.InputFiles) == 0
			ms.InputFiles = appendUniquePaths(ms.InputFiles, paths)
			if hadNone {
				ms.ActiveFileIdx = 0
				a.mu.Unlock()
				a.probeActiveFile(ctx, activeMode)
				return nil
			}
		}
	case "btn_clear_files":
		a.clearSessionLocked(activeMode)
		preview.CleanupAssets(a.assetsDir, "ff_"+activeMode+"_")

	case "active_file_select":
		if idx, err := strconv.Atoi(ev.Value); err == nil && idx >= 0 && idx < len(ms.InputFiles) {
			ms.ActiveFileIdx = idx
			a.mu.Unlock()
			a.probeActiveFile(ctx, activeMode)
			return nil
		}

	// Video Player Telemetry & Transport
	case "video_player":
		if ev.Kind == pluginapi.UIEventMedia {
			prevPlaying, prevEnded, prevReady, prevErr := ms.IsPlaying, ms.PlayEnded, ms.VideoReady, ms.VideoErr
			applyMediaTelemetry(ms, activeMode, ev.Value)
			stateChanged := ms.IsPlaying != prevPlaying || ms.PlayEnded != prevEnded || ms.VideoReady != prevReady || ms.VideoErr != prevErr
			if ms.VideoErr != "" && !ms.ProxyTried && s.HasFFmpeg && ms.Probe != nil {
				switch s.PreviewProxyPolicy {
				case ProxyPolicyAuto:
					a.startProxyBuildLocked(ctx, activeMode, ctrl.probeSeq, ms.Probe)
					stateChanged = true
				case ProxyPolicyRemuxOnly:
					if preview.CanFastRemuxProxy(ms.Probe) {
						a.startProxyBuildLocked(ctx, activeMode, ctrl.probeSeq, ms.Probe)
					} else {
						ms.ProxyRequired = true
						ms.TryNativeOverride = false
						ms.ProxyReason = "Native player could not decode this file (" + ms.VideoErr + ")."
					}
					stateChanged = true
				case ProxyPolicyManual:
					ms.ProxyRequired = true
					ms.TryNativeOverride = false
					ms.ProxyReason = "Native player could not decode this file (" + ms.VideoErr + ")."
					stateChanged = true
				}
			}
			if !stateChanged {
				a.mu.Unlock()
				return nil
			}
			forceRender = true
		}
	case "btn_load_preview":
		if ms.Probe != nil && s.VideoPreviewMode != VideoPreviewOff {
			a.activatePreviewLocked(ctx, activeMode)
		}
	case "btn_unload_preview":
		if ctrl.proxyCancel != nil {
			ctrl.proxyCancel()
			ctrl.proxyCancel = nil
		}
		ms.PreviewLoaded = false
		ms.ProxyLoading = false
		ms.ProxyRequired = false
		ms.IsPlaying = false
		s.FullscreenOpen = false
		s.PlaySeq++
	case "btn_build_proxy":
		if ms.Probe != nil && s.HasFFmpeg && a.assetsDir != "" {
			ms.PreviewLoaded = true
			ms.TryNativeOverride = false
			a.startProxyBuildLocked(ctx, activeMode, ctrl.probeSeq, ms.Probe)
		}
	case "btn_cancel_proxy":
		if ctrl.proxyCancel != nil {
			ctrl.proxyCancel()
			ctrl.proxyCancel = nil
		}
		ms.ProxyLoading = false
		ms.ProxyRequired = true
		ms.ProxyReason = "Preview proxy canceled. You can still configure settings and export, or rebuild anytime."
	case "btn_try_native":
		if ctrl.proxyCancel != nil {
			ctrl.proxyCancel()
			ctrl.proxyCancel = nil
		}
		ms.ProxyLoading = false
		ms.ProxyRequired = false
		ms.TryNativeOverride = true
		ms.VideoErr = ""
	case "btn_video_fit":
		if ms.VideoFit == "cover" {
			ms.VideoFit = "contain"
		} else {
			ms.VideoFit = "cover"
		}
	case "btn_play_pause", "fs_play_pause":
		a.togglePlayPauseLocked(ctx)
	case "btn_jump_in", "fs_jump_in":
		if modeUsesTrimRange(activeMode) {
			a.seekToLocked(ms.StartSec)
		} else {
			a.seekToLocked(0)
		}
	case "btn_jump_out", "fs_jump_out":
		if modeUsesTrimRange(activeMode) {
			a.seekToLocked(ms.EndSec)
		} else if ms.Probe != nil {
			a.seekToLocked(ms.Probe.DurationSec)
		}
	case "btn_skip_back", "fs_skip_back":
		a.seekToLocked(ms.PlayheadSec - 5)
	case "btn_skip_fwd", "fs_skip_fwd":
		a.seekToLocked(ms.PlayheadSec + 5)
	case "btn_step_back":
		a.stepFrameLocked(-1)
	case "btn_step_fwd":
		a.stepFrameLocked(+1)
	case "btn_mark_in", "fs_mark_in":
		if modeUsesTrimRange(activeMode) {
			a.markInAtPlayheadLocked()
		}
	case "btn_mark_out", "fs_mark_out":
		if modeUsesTrimRange(activeMode) {
			a.markOutAtPlayheadLocked()
		}
	case "btn_loop_toggle", "fs_loop_toggle":
		if modeUsesTrimRange(activeMode) {
			ms.LoopSelection = !ms.LoopSelection
		}
	case "btn_speed_cycle":
		ms.PlaybackRate = nextPlaybackRate(ms.PlaybackRate)
	case "btn_mute_toggle":
		ms.Muted = !ms.Muted
	case "vol_slider":
		if v, err := strconv.ParseFloat(ev.Value, 64); err == nil {
			ms.Volume = minFloat(1, maxFloat(0, v))
			if ms.Volume > 0 {
				ms.Muted = false
			}
			forceRender = false
		}
	case "btn_snapshot_hd", "fs_snapshot_hd":
		sec := ms.PlayheadSec
		a.mu.Unlock()
		a.saveHDFrameAsync(ctx, activeMode, sec, "frame")
		return nil
	case "btn_fullscreen":
		if ms.PreviewLoaded && !ms.ProxyLoading && !ms.ProxyRequired && s.VideoPreviewMode != VideoPreviewOff {
			s.FullscreenOpen = !s.FullscreenOpen
		}
	case "fs_dialog":
		if ev.Kind == pluginapi.UIEventClose {
			s.FullscreenOpen = false
		}

	// Modes
	case "mode_convert":
		a.switchModeLocked(engine.ModeConvert)
	case "mode_trim":
		a.switchModeLocked(engine.ModeTrim)
	case "mode_streams":
		a.switchModeLocked(engine.ModeStreams)
	case "mode_frames":
		a.switchModeLocked(engine.ModeFrames)

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

	// Trim & Playback controls
	case "trim_lossless":
		s.Opts.LosslessCut = ev.Value == "true"
	case "trim_concat_mode":
		s.Opts.ConcatMode = ev.Value
	case "trim_canvas", "playback_canvas":
		if ev.Kind == pluginapi.UIEventClick && ms.Probe != nil && ms.Probe.DurationSec > 0 {
			var xPx float64
			if _, err := fmt.Sscanf(ev.Value, "%f,", &xPx); err == nil {
				trackW := ComputeTrackWidth(s.ViewportW)
				a.seekToLocked(XToTime(xPx, ms.Probe.DurationSec, trackW))
			}
		}
	case "playhead":
		if ev.Kind == pluginapi.UIEventDragEnd && ms.Probe != nil && ms.Probe.DurationSec > 0 {
			var xPx float64
			_, _ = fmt.Sscanf(ev.Value, "%f,", &xPx)
			trackW := ComputeTrackWidth(s.ViewportW)
			a.seekToLocked(ApplyPlayheadDrag(xPx, ms.Probe.DurationSec, trackW))
		}
	case "trim_start", "trim_end":
		if ev.Kind == pluginapi.UIEventDragEnd && ms.Probe != nil && ms.Probe.DurationSec > 0 {
			var xPx float64
			_, _ = fmt.Sscanf(ev.Value, "%f,", &xPx)
			trackW := ComputeTrackWidth(s.ViewportW)
			ns, ne := ApplyHandleDrag(ev.Node, xPx, ms.StartSec, ms.EndSec, ms.Probe.DurationSec, trackW)
			ms.StartSec, ms.EndSec = ns, ne
			ms.StartText = FormatTimestamp(ns)
			ms.EndText = FormatTimestamp(ne)
			if ev.Node == "trim_start" {
				a.seekToLocked(ns)
			} else {
				a.seekToLocked(ne)
			}
		}
	case "trim_window":
		if ev.Kind == pluginapi.UIEventDragEnd && ms.Probe != nil && ms.Probe.DurationSec > 0 {
			var xPx float64
			_, _ = fmt.Sscanf(ev.Value, "%f,", &xPx)
			trackW := ComputeTrackWidth(s.ViewportW)
			ns, ne := ApplyWindowDrag(xPx-float64(HandleWidth)/2, ms.StartSec, ms.EndSec, ms.Probe.DurationSec, trackW)
			ms.StartSec, ms.EndSec = ns, ne
			ms.StartText = FormatTimestamp(ns)
			ms.EndText = FormatTimestamp(ne)
			a.seekToLocked(ns)
		}
	case "time_start_input":
		ms.StartText = ev.Value
		if sec, ok := ParseTimestamp(ev.Value); ok && ms.Probe != nil {
			if ms.Probe.DurationSec > 0 && sec > ms.Probe.DurationSec {
				sec = ms.Probe.DurationSec
			}
			if sec >= ms.EndSec {
				sec = maxFloat(0, ms.EndSec-0.5)
			}
			ms.StartSec = sec
			a.seekToLocked(sec)
		}
	case "time_end_input":
		ms.EndText = ev.Value
		if sec, ok := ParseTimestamp(ev.Value); ok && ms.Probe != nil {
			if ms.Probe.DurationSec > 0 && sec > ms.Probe.DurationSec {
				sec = ms.Probe.DurationSec
			}
			if sec <= ms.StartSec {
				sec = ms.StartSec + 0.5
			}
			ms.EndSec = sec
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
		ms.StartSec = 0
		if ms.Probe != nil && ms.Probe.DurationSec > 0 {
			ms.EndSec = ms.Probe.DurationSec
			ms.StartText = FormatTimestamp(0)
			ms.EndText = FormatTimestamp(ms.Probe.DurationSec)
		} else {
			ms.EndSec = 0
			ms.StartText = ""
			ms.EndText = ""
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
			a.probeActiveFile(ctx, activeMode)
			return nil
		}
	}

	tree := RenderTree(a.state)
	a.mu.Unlock()
	a.renderTree(tree, forceRender)
	return nil
}

func (a *App) switchModeLocked(nextMode string) {
	s := &a.state
	next := normalizeMode(nextMode)
	cur := normalizeMode(s.Mode)
	if next == cur {
		return
	}
	prevSess := s.SessionFor(cur)
	prevSess.IsPlaying = false
	s.FullscreenOpen = false
	s.Mode = next
	nextSess := s.SessionFor(next)
	nextSess.IsPlaying = false
	s.PlaySeq++
	if nextSess.Probe != nil {
		nextSess.SeekTargetSec = nextSess.PlayheadSec
		s.SeekSeq++
	}
}

func (a *App) clearSessionLocked(mode string) {
	ctrl := a.workerCtrlLocked(mode)
	if ctrl.probeCancel != nil {
		ctrl.probeCancel()
		ctrl.probeCancel = nil
	}
	if ctrl.stripCancel != nil {
		ctrl.stripCancel()
		ctrl.stripCancel = nil
	}
	if ctrl.proxyCancel != nil {
		ctrl.proxyCancel()
		ctrl.proxyCancel = nil
	}
	ctrl.probeSeq++
	ms := a.state.SessionFor(mode)
	rate := ms.PlaybackRate
	if rate <= 0 {
		rate = 1.0
	}
	vol := ms.Volume
	muted := ms.Muted
	fit := ms.VideoFit
	*ms = defaultModeSession()
	ms.PlaybackRate = rate
	ms.Volume = vol
	ms.Muted = muted
	if fit != "" {
		ms.VideoFit = fit
	}
}

func (a *App) handleShortcutLocked(ctx context.Context, spec string) bool {
	s := &a.state
	activeMode := normalizeMode(s.Mode)
	ms := s.SessionFor(activeMode)
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "escape":
		s.FullscreenOpen = false
	case "space", "k":
		a.togglePlayPauseLocked(ctx)
	case "j":
		a.seekToLocked(ms.PlayheadSec - 5)
	case "l":
		a.seekToLocked(ms.PlayheadSec + 5)
	case "i":
		if modeUsesTrimRange(activeMode) {
			a.markInAtPlayheadLocked()
		}
	case "o":
		if modeUsesTrimRange(activeMode) {
			a.markOutAtPlayheadLocked()
		}
	case "home":
		if modeUsesTrimRange(activeMode) {
			a.seekToLocked(ms.StartSec)
		}
	case "end":
		if modeUsesTrimRange(activeMode) {
			a.seekToLocked(ms.EndSec)
		}
	case ",":
		a.stepFrameLocked(-1)
	case ".":
		a.stepFrameLocked(+1)
	case "m":
		ms.Muted = !ms.Muted
	case "f":
		if ms.Probe != nil && ms.Probe.HasVideo() && ms.PreviewLoaded && !ms.ProxyLoading && !ms.ProxyRequired && s.VideoPreviewMode != VideoPreviewOff {
			s.FullscreenOpen = !s.FullscreenOpen
		}
	case "shift+l":
		if modeUsesTrimRange(activeMode) {
			ms.LoopSelection = !ms.LoopSelection
		}
	case "s":
		if ms.Probe != nil && ms.Probe.HasVideo() && s.HasFFmpeg {
			sec := ms.PlayheadSec
			a.mu.Unlock()
			a.saveHDFrameAsync(ctx, activeMode, sec, "frame")
			a.mu.Lock()
			return true
		}
	}
	return false
}

func (a *App) handleDynamicNodeLocked(ev pluginapi.UIEvent) bool {
	s := &a.state
	activeMode := normalizeMode(s.Mode)
	ms := s.SessionFor(activeMode)
	switch {
	case strings.HasPrefix(ev.Node, "preset_"):
		ApplyConvertPreset(s, strings.TrimPrefix(ev.Node, "preset_"))
	case strings.HasPrefix(ev.Node, "file_select_"):
		if idx, err := strconv.Atoi(strings.TrimPrefix(ev.Node, "file_select_")); err == nil && idx >= 0 && idx < len(ms.InputFiles) {
			ms.ActiveFileIdx = idx
			return true
		}
	case strings.HasPrefix(ev.Node, "file_remove_"):
		if idx, err := strconv.Atoi(strings.TrimPrefix(ev.Node, "file_remove_")); err == nil && idx >= 0 && idx < len(ms.InputFiles) {
			wasActive := idx == ms.ActiveFileIdx
			ms.InputFiles = append(ms.InputFiles[:idx], ms.InputFiles[idx+1:]...)
			if len(ms.InputFiles) == 0 {
				a.clearSessionLocked(activeMode)
				preview.CleanupAssets(a.assetsDir, "ff_"+activeMode+"_")
				return false
			}
			if ms.ActiveFileIdx >= len(ms.InputFiles) {
				ms.ActiveFileIdx = len(ms.InputFiles) - 1
			}
			return wasActive
		}
	case strings.HasPrefix(ev.Node, "clip_select_"):
		id := strings.TrimPrefix(ev.Node, "clip_select_")
		for _, cl := range ms.Clips {
			if cl.ID == id {
				ms.StartSec = cl.StartSec
				ms.EndSec = cl.EndSec
				ms.StartText = FormatTimestamp(cl.StartSec)
				ms.EndText = FormatTimestamp(cl.EndSec)
				a.seekToLocked(cl.StartSec)
				break
			}
		}
	case strings.HasPrefix(ev.Node, "clip_remove_"):
		id := strings.TrimPrefix(ev.Node, "clip_remove_")
		filtered := ms.Clips[:0]
		for _, cl := range ms.Clips {
			if cl.ID != id {
				filtered = append(filtered, cl)
			}
		}
		ms.Clips = filtered
	case strings.HasPrefix(ev.Node, "q_remove_"):
		id := strings.TrimPrefix(ev.Node, "q_remove_")
		a.queueMgr.Remove(id)
	}
	return false
}

func (a *App) togglePlayPauseLocked(ctx context.Context) {
	s := &a.state
	activeMode := normalizeMode(s.Mode)
	ms := s.SessionFor(activeMode)
	if ms.Probe == nil || s.VideoPreviewMode == VideoPreviewOff {
		return
	}
	if !ms.PreviewLoaded {
		a.activatePreviewLocked(ctx, activeMode)
		if ms.ProxyLoading || ms.ProxyRequired {
			return
		}
	}
	if !ms.IsPlaying {
		if modeUsesTrimRange(s.Mode) && ms.EndSec > ms.StartSec {
			if ms.PlayEnded || ms.PlayheadSec < ms.StartSec-0.05 || ms.PlayheadSec >= ms.EndSec-0.05 {
				a.seekToLocked(ms.StartSec)
			}
		} else if ms.PlayEnded {
			a.seekToLocked(0)
		}
		ms.PlayEnded = false
		ms.IsPlaying = true
		s.PlaySeq++
		return
	}
	ms.IsPlaying = false
	s.PlaySeq++
}

func (a *App) seekToLocked(sec float64) {
	s := &a.state
	ms := s.ActiveSession()
	if ms.Probe == nil {
		return
	}
	dur := ms.Probe.DurationSec
	if sec < 0 {
		sec = 0
	}
	if dur > 0 && sec > dur {
		sec = dur
	}
	upperBound := dur
	if modeUsesTrimRange(s.Mode) && ms.EndSec > ms.StartSec {
		if sec < ms.StartSec {
			sec = ms.StartSec
		}
		if sec > ms.EndSec {
			sec = ms.EndSec
		}
		upperBound = ms.EndSec
	}
	ms.PlayheadSec = sec
	ms.SeekTargetSec = sec
	s.SeekSeq++
	if upperBound <= 0 || sec < upperBound-0.05 {
		ms.PlayEnded = false
	}
}

func (a *App) stepFrameLocked(dir int) {
	s := &a.state
	ms := s.ActiveSession()
	if ms.Probe == nil {
		return
	}
	if ms.IsPlaying {
		ms.IsPlaying = false
		s.PlaySeq++
	}
	step := 0.04 // 25fps fallback
	if ms.Probe.HasVideo() && ms.Probe.VideoStreams[0].FPS > 0 {
		step = 1.0 / ms.Probe.VideoStreams[0].FPS
	}
	a.seekToLocked(ms.PlayheadSec + float64(dir)*step)
}

func (a *App) markInAtPlayheadLocked() {
	s := &a.state
	ms := s.ActiveSession()
	if ms.Probe == nil || ms.Probe.DurationSec <= 0 {
		return
	}
	ns := maxFloat(0, minFloat(ms.PlayheadSec, ms.EndSec-0.5))
	ms.StartSec = ns
	ms.StartText = FormatTimestamp(ns)
}

func (a *App) markOutAtPlayheadLocked() {
	s := &a.state
	ms := s.ActiveSession()
	if ms.Probe == nil || ms.Probe.DurationSec <= 0 {
		return
	}
	ne := minFloat(ms.Probe.DurationSec, maxFloat(ms.PlayheadSec, ms.StartSec+0.5))
	ms.EndSec = ne
	ms.EndText = FormatTimestamp(ne)
}

func (a *App) nudgeStartLocked(delta float64) {
	s := &a.state
	ms := s.ActiveSession()
	if ms.Probe == nil || ms.Probe.DurationSec <= 0 {
		return
	}
	ns := ms.StartSec + delta
	if ns < 0 {
		ns = 0
	}
	if ns > ms.EndSec-0.5 {
		ns = maxFloat(0, ms.EndSec-0.5)
	}
	ms.StartSec = ns
	ms.StartText = FormatTimestamp(ns)
	a.seekToLocked(ns)
}

func (a *App) nudgeEndLocked(delta float64) {
	s := &a.state
	ms := s.ActiveSession()
	if ms.Probe == nil || ms.Probe.DurationSec <= 0 {
		return
	}
	ne := ms.EndSec + delta
	if ne > ms.Probe.DurationSec {
		ne = ms.Probe.DurationSec
	}
	if ne < ms.StartSec+0.5 {
		ne = minFloat(ms.Probe.DurationSec, ms.StartSec+0.5)
	}
	ms.EndSec = ne
	ms.EndText = FormatTimestamp(ne)
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
	a.state.VideoPreviewMode = normalizeVideoPreviewMode(settings["video_preview"])
	a.state.TimelineFilmstrip = strings.TrimSpace(settings["timeline_filmstrip"]) != "false"
	a.state.PreviewProxyPolicy = normalizeProxyPolicy(settings["preview_proxy"])
	a.state.HWAccelMode = engine.NormalizeHWAccelMode(settings["hardware_accel"])
	a.state.HWBackend = engine.NormalizeHWBackend(settings["hardware_backend"])
}

func (a *App) reloadSettingsFromDiskLocked(force bool) bool {
	if a.settingsFile == "" {
		return false
	}
	info, err := os.Stat(a.settingsFile)
	if err != nil {
		return false
	}
	if !force && !a.settingsModTime.IsZero() && info.ModTime().Equal(a.settingsModTime) {
		return false
	}
	data, err := os.ReadFile(a.settingsFile)
	if err != nil {
		return false
	}
	var loaded map[string]any
	if err := json.Unmarshal(data, &loaded); err != nil {
		return false
	}
	merged := make(map[string]string, len(a.state.Settings)+len(loaded))
	for k, v := range a.state.Settings {
		merged[k] = v
	}
	for k, v := range loaded {
		switch tv := v.(type) {
		case string:
			merged[k] = tv
		case bool:
			merged[k] = strconv.FormatBool(tv)
		}
	}
	if merged["ffmpeg_path"] != a.state.Settings["ffmpeg_path"] {
		a.ffCache = cachedBin{}
		a.hwCache = cachedHWCaps{}
	}
	if merged["ffprobe_path"] != a.state.Settings["ffprobe_path"] {
		a.fpCache = cachedBin{}
	}
	a.settingsModTime = info.ModTime()
	a.applySettingsLocked(merged)
	return true
}

func (a *App) reconcileSessionsWithSettingsLocked(ctx context.Context) {
	allModes := []string{engine.ModeConvert, engine.ModeTrim, engine.ModeStreams, engine.ModeFrames}
	activeMode := normalizeMode(a.state.Mode)
	for _, m := range allModes {
		ms := a.state.SessionFor(m)
		ctrl := a.workerCtrlLocked(m)
		if a.state.VideoPreviewMode == VideoPreviewOff {
			if ctrl.proxyCancel != nil {
				ctrl.proxyCancel()
				ctrl.proxyCancel = nil
			}
			ms.PreviewLoaded = false
			ms.ProxyLoading = false
			ms.ProxyRequired = false
			ms.IsPlaying = false
			if m == activeMode {
				a.state.FullscreenOpen = false
				a.state.PlaySeq++
			}
		}
		if !a.state.TimelineFilmstrip {
			if ctrl.stripCancel != nil {
				ctrl.stripCancel()
				ctrl.stripCancel = nil
			}
			ms.FilmstripLoading = false
			ms.StripAsset = ""
		}
	}

	ms := a.state.SessionFor(activeMode)
	ctrl := a.workerCtrlLocked(activeMode)
	if ms.Probe == nil || ms.ProbeLoading {
		return
	}
	if a.state.TimelineFilmstrip && modeUsesTrimRange(activeMode) && ms.Probe.HasVideo() && ms.Probe.DurationSec > 0 && ms.StripAsset == "" && !ms.FilmstripLoading && a.assetsDir != "" && a.state.HasFFmpeg {
		a.startFilmstripBuildLocked(ctx, activeMode, ctrl.probeSeq, ms.Probe)
	}
	if shouldAutoPreview(a.state.VideoPreviewMode, activeMode) && !ms.PreviewLoaded {
		a.activatePreviewLocked(ctx, activeMode)
	}
	if ms.PreviewLoaded && ms.ProxyRequired && !ms.ProxyLoading && ms.ProxyAsset == "" {
		switch a.state.PreviewProxyPolicy {
		case ProxyPolicyOff:
			ms.ProxyRequired = false
			ms.TryNativeOverride = true
		case ProxyPolicyAuto:
			if a.assetsDir != "" && a.state.HasFFmpeg {
				a.startProxyBuildLocked(ctx, activeMode, ctrl.probeSeq, ms.Probe)
			}
		case ProxyPolicyRemuxOnly:
			if preview.CanFastRemuxProxy(ms.Probe) && a.assetsDir != "" && a.state.HasFFmpeg {
				a.startProxyBuildLocked(ctx, activeMode, ctrl.probeSeq, ms.Probe)
			}
		}
	}
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
	if a.state.HasFFmpeg {
		if !a.hwCache.checked || a.hwCache.ffPath != a.state.FFmpegPath {
			a.hwCache = cachedHWCaps{
				ffPath:  a.state.FFmpegPath,
				caps:    engine.ProbeHWCapabilities(context.Background(), a.state.FFmpegPath),
				checked: true,
			}
		}
		a.state.HWCaps = a.hwCache.caps
	} else {
		a.state.HWCaps = engine.HWCapabilities{}
	}
	a.state.HWPlan = engine.ResolveHWPlan(a.state.HWAccelMode, a.state.HWBackend, a.state.HWCaps, runtime.GOOS)
}

func (a *App) updateLoadingStageLocked(mode string) {
	ms := a.state.SessionFor(mode)
	if ms.ProbeLoading {
		ms.LoadingStage = "Inspecting media streams…"
		ms.LoadingPct = 25
		return
	}
	ms.LoadingStage = ""
	ms.LoadingPct = 100
}

func (a *App) cancelLoadingLocked(mode string) {
	ctrl := a.workerCtrlLocked(mode)
	if ctrl.probeCancel != nil {
		ctrl.probeCancel()
		ctrl.probeCancel = nil
	}
	if ctrl.stripCancel != nil {
		ctrl.stripCancel()
		ctrl.stripCancel = nil
	}
	if ctrl.proxyCancel != nil {
		ctrl.proxyCancel()
		ctrl.proxyCancel = nil
	}
	ctrl.probeSeq++
	ms := a.state.SessionFor(mode)
	ms.ProbeLoading = false
	ms.FilmstripLoading = false
	ms.ProxyLoading = false
	ms.PreviewLoaded = false
	ms.ProxyRequired = false
	ms.ProxyReason = ""
	ms.ProxyStage = ""
	ms.TryNativeOverride = false
	ms.LoadingStage = ""
	ms.LoadingFile = ""
	ms.LoadingPct = 0
	ms.IsPlaying = false
	ms.PlayEnded = false
	ms.PlayheadSec = 0
	ms.VideoReady = false
	ms.VideoErr = ""
	ms.Probe = nil
	ms.StripAsset = ""
	ms.PosterAsset = ""
	ms.ProxyAsset = ""
	a.state.StatusMsg = "Media loading canceled."
}

func (a *App) probeActiveFile(parentCtx context.Context, mode string) {
	m := normalizeMode(mode)
	a.mu.Lock()
	ms := a.state.SessionFor(m)
	if len(ms.InputFiles) == 0 || ms.ActiveFileIdx >= len(ms.InputFiles) {
		a.mu.Unlock()
		return
	}
	if !a.state.HasFFmpeg {
		ms.ProbeError = "FFmpeg is required to inspect media files. Click Download FFmpeg above."
		tree := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(tree, true)
		return
	}
	ctrl := a.workerCtrlLocked(m)
	if ctrl.probeCancel != nil {
		ctrl.probeCancel()
	}
	if ctrl.stripCancel != nil {
		ctrl.stripCancel()
		ctrl.stripCancel = nil
	}
	if ctrl.proxyCancel != nil {
		ctrl.proxyCancel()
		ctrl.proxyCancel = nil
	}
	pctx, cancel := context.WithCancel(parentCtx)
	ctrl.probeCancel = cancel
	ctrl.probeSeq++
	seq := ctrl.probeSeq

	filePath := ms.InputFiles[ms.ActiveFileIdx]
	ffBin := a.state.FFmpegPath
	fpBin := a.state.FFprobePath
	assetsDir := a.assetsDir
	ms.ProbeLoading = true
	ms.FilmstripLoading = false
	ms.PreviewLoaded = false
	ms.ProxyRequired = false
	ms.ProxyReason = ""
	ms.ProxyStage = ""
	ms.TryNativeOverride = false
	ms.ProbeError = ""
	a.state.ErrorMsg = ""
	ms.IsPlaying = false
	ms.PlayEnded = false
	ms.PlayheadSec = 0
	ms.SeekTargetSec = 0
	a.state.PlaySeq++
	a.state.SeekSeq++
	ms.VideoReady = false
	ms.VideoErr = ""
	ms.ProxyAsset = ""
	ms.StripAsset = ""
	ms.PosterAsset = ""
	ms.ProxyLoading = false
	ms.ProxyTried = false
	a.state.FullscreenOpen = false
	ms.LoadingFile = filepath.Base(filePath)
	a.updateLoadingStageLocked(m)
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
			curCtrl := a.workerCtrlLocked(m)
			curMS := a.state.SessionFor(m)
			if seq == curCtrl.probeSeq {
				curMS.ProbeLoading = false
				curMS.FilmstripLoading = false
				curMS.ProxyLoading = false
				a.updateLoadingStageLocked(m)
				curMS.ProbeError = err.Error()
				a.state.StatusMsg = ""
				t := RenderTree(a.state)
				a.mu.Unlock()
				a.renderTree(t, true)
				return
			}
			a.mu.Unlock()
			return
		}

		preview.CleanupAssets(assetsDir, "ff_"+m+"_")
		a.mu.Lock()
		curCtrl := a.workerCtrlLocked(m)
		if seq != curCtrl.probeSeq {
			a.mu.Unlock()
			return
		}
		curMS := a.state.SessionFor(m)
		curMS.ProbeLoading = false
		curMS.Probe = probe
		curMS.StartSec = 0
		curMS.EndSec = probe.DurationSec
		curMS.StartText = FormatTimestamp(0)
		curMS.EndText = FormatTimestamp(probe.DurationSec)
		curMS.Clips = nil
		a.state.StatusMsg = ""
		a.updateLoadingStageLocked(m)

		if a.state.TimelineFilmstrip && modeUsesTrimRange(m) && probe.HasVideo() && probe.DurationSec > 0 && assetsDir != "" {
			a.startFilmstripBuildLocked(parentCtx, m, seq, probe)
		}
		if shouldAutoPreview(a.state.VideoPreviewMode, m) {
			a.activatePreviewLocked(parentCtx, m)
		}
		initialTree := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(initialTree, true)
	}()
}

func (a *App) startFilmstripBuildLocked(parentCtx context.Context, mode string, seq uint64, probe *engine.MediaProbe) {
	m := normalizeMode(mode)
	ms := a.state.SessionFor(m)
	if a.assetsDir == "" || !a.state.HasFFmpeg || probe == nil || !probe.HasVideo() || probe.DurationSec <= 0 {
		return
	}
	ctrl := a.workerCtrlLocked(m)
	if ctrl.stripCancel != nil {
		ctrl.stripCancel()
	}
	sctx, cancel := context.WithCancel(parentCtx)
	ctrl.stripCancel = cancel
	a.state.AssetRev++
	rev := a.state.AssetRev
	ms.FilmstripLoading = true

	ffBin := a.state.FFmpegPath
	assetsDir := a.assetsDir
	filePath := probe.Path
	dur := probe.DurationSec
	hwPlan := a.state.HWPlan
	trackW := ComputeTrackWidth(a.state.ViewportW)
	numTiles := clampIntRange(int(math.Round(float64(trackW)/110.0)), 8, 12)
	tileW := maxInt(80, int(math.Round(float64(trackW)/float64(numTiles))))
	const tileH = 54

	go func() {
		defer cancel()
		sName := fmt.Sprintf("ff_%s_strip_%d.jpg", m, rev)
		var stripAsset string
		if preview.BuildFilmstripWithHW(sctx, ffBin, filePath, dur, numTiles, tileW, tileH, hwPlan, filepath.Join(assetsDir, sName)) == nil {
			stripAsset = sName
		}
		if sctx.Err() != nil {
			return
		}
		a.mu.Lock()
		c2 := a.workerCtrlLocked(m)
		if seq != c2.probeSeq {
			a.mu.Unlock()
			return
		}
		ms2 := a.state.SessionFor(m)
		ms2.StripAsset = stripAsset
		ms2.FilmstripLoading = false
		t := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(t, true)
	}()
}

func (a *App) activatePreviewLocked(parentCtx context.Context, mode string) {
	m := normalizeMode(mode)
	if a.state.VideoPreviewMode == VideoPreviewOff {
		return
	}
	ms := a.state.SessionFor(m)
	if ms.Probe == nil {
		return
	}
	ms.PreviewLoaded = true
	ms.VideoErr = ""
	ms.ProxyRequired = false
	ms.ProxyReason = ""
	if ms.ProxyAsset != "" || ms.TryNativeOverride || !preview.NeedsPreviewProxy(ms.Probe) || a.assetsDir == "" || !a.state.HasFFmpeg {
		return
	}
	ctrl := a.workerCtrlLocked(m)
	switch a.state.PreviewProxyPolicy {
	case ProxyPolicyOff:
		return
	case ProxyPolicyAuto:
		a.startProxyBuildLocked(parentCtx, m, ctrl.probeSeq, ms.Probe)
	case ProxyPolicyRemuxOnly:
		if preview.CanFastRemuxProxy(ms.Probe) {
			a.startProxyBuildLocked(parentCtx, m, ctrl.probeSeq, ms.Probe)
		} else {
			ms.ProxyRequired = true
			ms.ProxyReason = describeProxyRequirement(ms.Probe)
		}
	case ProxyPolicyManual:
		ms.ProxyRequired = true
		ms.ProxyReason = describeProxyRequirement(ms.Probe)
	}
}

func describeProxyRequirement(probe *engine.MediaProbe) string {
	if probe == nil {
		return "This format needs a lightweight MP4 preview proxy for OS playback."
	}
	ext := strings.ToUpper(strings.TrimPrefix(filepath.Ext(probe.Path), "."))
	if ext == "" {
		ext = "Media"
	}
	if probe.SizeBytes > preview.MaxRemuxProxyBytes {
		return fmt.Sprintf("%s (%s) exceeds the 240 MB fast-remux limit. Build a fast 480p proxy or export directly.", ext, formatBytesHuman(probe.SizeBytes))
	}
	if probe.HasVideo() {
		vc := strings.ToUpper(probe.VideoStreams[0].Codec)
		return fmt.Sprintf("%s (%s) needs a 480p H.264 preview proxy for smooth playback. You can still configure and export immediately.", ext, vc)
	}
	return fmt.Sprintf("%s audio needs a quick AAC preview proxy, or you can export directly.", ext)
}

func (a *App) startProxyBuildLocked(parentCtx context.Context, mode string, seq uint64, probe *engine.MediaProbe) {
	m := normalizeMode(mode)
	ms := a.state.SessionFor(m)
	if a.assetsDir == "" || !a.state.HasFFmpeg || probe == nil || ms.ProxyLoading {
		return
	}
	ctrl := a.workerCtrlLocked(m)
	if ctrl.proxyCancel != nil {
		ctrl.proxyCancel()
	}
	xctx, cancel := context.WithCancel(parentCtx)
	ctrl.proxyCancel = cancel
	a.state.AssetRev++
	rev := a.state.AssetRev
	ms.ProxyRequired = false
	ms.ProxyReason = ""
	ms.ProxyLoading = true
	ms.ProxyTried = true
	hwPlan := a.state.HWPlan
	switch {
	case !probe.HasVideo():
		ms.ProxyStage = "Preparing audio preview…"
	case preview.CanFastRemuxProxy(probe):
		ms.ProxyStage = "Remuxing container for preview…"
	case hwPlan.Active():
		ms.ProxyStage = "Building 480p GPU preview proxy…"
	default:
		ms.ProxyStage = "Building 480p preview proxy…"
	}
	ffBin := a.state.FFmpegPath
	assetsDir := a.assetsDir
	filePath := probe.Path

	go func() {
		defer cancel()
		proxyName := fmt.Sprintf("ff_%s_proxy_%d.mp4", m, rev)
		err := preview.BuildPlayableProxyWithHW(xctx, ffBin, filePath, probe, hwPlan, filepath.Join(assetsDir, proxyName))
		if xctx.Err() != nil {
			return
		}
		a.mu.Lock()
		c2 := a.workerCtrlLocked(m)
		if seq != c2.probeSeq {
			a.mu.Unlock()
			return
		}
		ms2 := a.state.SessionFor(m)
		ms2.ProxyLoading = false
		ms2.ProxyStage = ""
		if err == nil {
			ms2.ProxyAsset = proxyName
			ms2.ProxyRequired = false
			ms2.ProxyReason = ""
			ms2.VideoErr = ""
		} else {
			ms2.ProxyRequired = true
			ms2.ProxyReason = "Preview proxy failed: " + err.Error()
			ms2.VideoErr = ms2.ProxyReason
		}
		t := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(t, true)
	}()
}

func (a *App) addCurrentClipLocked() {
	s := &a.state
	ms := s.ActiveSession()
	if ms.Probe == nil || ms.EndSec <= ms.StartSec {
		return
	}
	ms.NextClipID++
	ms.Clips = append(ms.Clips, engine.ClipSegment{
		ID:       fmt.Sprintf("c%d", ms.NextClipID),
		Title:    fmt.Sprintf("Clip %d", ms.NextClipID),
		StartSec: ms.StartSec,
		EndSec:   ms.EndSec,
	})
	s.StatusMsg = fmt.Sprintf("Added Clip %d (%s–%s)", ms.NextClipID, FormatTimestamp(ms.StartSec), FormatTimestamp(ms.EndSec))
}

func (a *App) splitChaptersLocked() {
	s := &a.state
	ms := s.ActiveSession()
	if ms.Probe == nil || len(ms.Probe.Chapters) == 0 {
		return
	}
	ms.Clips = nil
	for i, ch := range ms.Probe.Chapters {
		ms.NextClipID++
		end := ch.EndSec
		if end <= ch.StartSec {
			end = ms.Probe.DurationSec
		}
		ms.Clips = append(ms.Clips, engine.ClipSegment{
			ID:       fmt.Sprintf("ch%d_%d", i+1, ms.NextClipID),
			Title:    ch.Title,
			StartSec: ch.StartSec,
			EndSec:   end,
		})
	}
	s.StatusMsg = fmt.Sprintf("Split %d chapters into cut list", len(ms.Clips))
}

func (a *App) saveHDFrameAsync(ctx context.Context, mode string, sec float64, tag string) {
	a.mu.Lock()
	ms := a.state.SessionFor(mode)
	if !a.state.HasFFmpeg || ms.Probe == nil {
		a.state.ErrorMsg = "FFmpeg and a loaded video are required for HD snapshots."
		t := RenderTree(a.state)
		a.mu.Unlock()
		a.renderTree(t, true)
		return
	}
	ffBin := a.state.FFmpegPath
	filePath := ms.Probe.Path
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
	plan, err := engine.BuildCommandPlan(spec, a.state.CurrentSession().Probe)
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

func applyMediaTelemetry(ms *ModeSession, mode, raw string) {
	for _, part := range strings.Split(raw, "|") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch k {
		case "pos":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
				if modeUsesTrimRange(mode) && ms.EndSec > ms.StartSec {
					if f < ms.StartSec {
						f = ms.StartSec
					} else if f > ms.EndSec {
						f = ms.EndSec
					}
				}
				ms.PlayheadSec = f
			}
		case "playing":
			ms.IsPlaying = v == "1"
		case "ended":
			ms.PlayEnded = v == "1"
		case "ready":
			ms.VideoReady = v == "1"
		case "err":
			ms.VideoErr = v
		}
	}
}

func clampIntRange(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
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
