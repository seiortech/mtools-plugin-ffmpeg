package liveui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"
	"github.com/seiortech/mtools-sdk/pluginapi"
)

func sampleVideoProbe() *engine.MediaProbe {
	return &engine.MediaProbe{
		Path:        "/tmp/demo.mp4",
		DurationSec: 120,
		SizeBytes:   25 * 1024 * 1024,
		VideoStreams: []engine.VideoStreamInfo{
			{Index: 0, Codec: "h264", Width: 1920, Height: 1080, FPS: 60},
		},
		AudioStreams: []engine.AudioStreamInfo{
			{Index: 1, Codec: "aac", Channels: 2, Language: "eng"},
		},
		Chapters: []engine.ChapterInfo{
			{ID: 1, Title: "Intro", StartSec: 0, EndSec: 30},
		},
	}
}

func TestRenderTreeAllStatesValid(t *testing.T) {
	app := NewApp(t.TempDir())
	defer app.Close()

	// 1. Empty state (MissingFFmpeg + Empty)
	s := app.state
	s.HasFFmpeg = false
	tree := RenderTree(s)
	tree.Rev = 1
	if err := tree.Validate(); err != nil {
		t.Fatalf("empty/missing-ffmpeg tree invalid: %v", err)
	}

	// 2. Loaded video across all 4 modes + all sub-tabs
	s.HasFFmpeg = true
	for _, mode := range []string{engine.ModeConvert, engine.ModeTrim, engine.ModeStreams, engine.ModeFrames} {
		ms := s.SessionFor(mode)
		ms.InputFiles = []string{"/tmp/demo.mp4", "/tmp/demo2.mkv"}
		ms.Probe = sampleVideoProbe()
		ms.PreviewLoaded = true
		ms.PosterAsset = "ff_poster_1.jpg"
		ms.StripAsset = "ff_strip_1.jpg"
		ms.StartSec = 10
		ms.EndSec = 45
		ms.PlayheadSec = 22.5
		ms.StartText = FormatTimestamp(10)
		ms.EndText = FormatTimestamp(45)
	}

	rev := uint64(2)
	for _, mode := range []string{engine.ModeConvert, engine.ModeTrim, engine.ModeStreams, engine.ModeFrames} {
		s.Mode = mode
		tr := RenderTree(s)
		tr.Rev = rev
		rev++
		if err := tr.Validate(); err != nil {
			t.Fatalf("mode %s tree invalid: %v", mode, err)
		}
	}

	s.Mode = engine.ModeConvert
	s.ShowCmdPreview = true
	for _, sub := range []string{"presets", "format", "quality", "output"} {
		s.ConvertSubTab = sub
		tr := RenderTree(s)
		tr.Rev = rev
		rev++
		if err := tr.Validate(); err != nil {
			t.Fatalf("convert subtab %s tree invalid: %v", sub, err)
		}
	}

	s.Mode = engine.ModeStreams
	for _, sub := range []string{"audio", "subtitles", "metadata"} {
		s.StreamsSubTab = sub
		tr := RenderTree(s)
		tr.Rev = rev
		rev++
		if err := tr.Validate(); err != nil {
			t.Fatalf("streams subtab %s tree invalid: %v", sub, err)
		}
	}

	// 3. Files, Queue, Collapsed Rail, Playing, Ended, Proxy, Fullscreen
	for _, tab := range []string{"files", "queue"} {
		s.ActiveTab = tab
		tr := RenderTree(s)
		tr.Rev = rev
		rev++
		if err := tr.Validate(); err != nil {
			t.Fatalf("sidebar tab %s tree invalid: %v", tab, err)
		}
	}

	s.ActiveTab = "inspector"
	s.RailCollapsed = true
	s.Mode = engine.ModeTrim
	trimMS := s.SessionFor(engine.ModeTrim)
	trimMS.IsPlaying = true
	trimMS.LoopSelection = true
	trimMS.ProxyAsset = "ff_proxy_1.mp4"
	s.FullscreenOpen = true
	tr := RenderTree(s)
	tr.Rev = rev
	rev++
	if err := tr.Validate(); err != nil {
		t.Fatalf("fullscreen/collapsed/proxy tree invalid: %v", err)
	}

	// 4. Audio-only media
	s.FullscreenOpen = false
	s.RailCollapsed = false
	trimMS.Probe = &engine.MediaProbe{
		Path:         "/tmp/song.mp3",
		DurationSec:  180,
		SizeBytes:    4 * 1024 * 1024,
		AudioStreams: []engine.AudioStreamInfo{{Index: 0, Codec: "mp3", Channels: 2}},
	}
	tr = RenderTree(s)
	tr.Rev = rev
	if err := tr.Validate(); err != nil {
		t.Fatalf("audio-only tree invalid: %v", err)
	}

	// 5. Responsive pane viewports (944x708, 664x648, 484x468, 1364x848)
	trimMS.Probe = sampleVideoProbe()
	for _, vp := range [][2]int{{944, 708}, {664, 648}, {484, 468}, {1364, 848}} {
		s.ViewportW, s.ViewportH = vp[0], vp[1]
		tr = RenderTree(s)
		rev++
		tr.Rev = rev
		if err := tr.Validate(); err != nil {
			t.Fatalf("viewport %dx%d tree invalid: %v", vp[0], vp[1], err)
		}
		tw := ComputeTrackWidth(vp[0])
		if tw > vp[0] {
			t.Fatalf("ComputeTrackWidth(%d) = %d exceeds pane width", vp[0], tw)
		}
	}
}

func TestStudioTransportAndSidebarEvents(t *testing.T) {
	app := NewApp(t.TempDir())
	defer app.Close()

	app.mu.Lock()
	app.state.HasFFmpeg = true
	app.state.Mode = engine.ModeTrim
	ms := app.state.SessionFor(engine.ModeTrim)
	ms.InputFiles = []string{"/tmp/demo.mp4"}
	ms.Probe = sampleVideoProbe()
	ms.PreviewLoaded = true
	ms.StartSec = 5
	ms.EndSec = 60
	ms.StartText = FormatTimestamp(5)
	ms.EndText = FormatTimestamp(60)
	app.mu.Unlock()

	ctx := context.Background()
	// Play -> Pause -> Step -> Mark IN/OUT -> Timeline click -> Subtab switch
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 1, Node: "btn_play_pause", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	if !ms.IsPlaying || app.state.PlaySeq != 1 {
		t.Fatalf("expected IsPlaying=true PlaySeq=1, got %v %d", ms.IsPlaying, app.state.PlaySeq)
	}
	app.mu.Unlock()

	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 2, Node: "video_player", Kind: pluginapi.UIEventMedia, Value: "pos=20.500|dur=120.000|playing=1|ended=0|ready=1|err="}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 3, Node: "btn_mark_in", Kind: pluginapi.UIEventClick}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 4, Node: "btn_step_fwd", Kind: pluginapi.UIEventClick}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 5, Node: "conv_subtabs", Kind: pluginapi.UIEventSelect, Value: "format"}, nil)

	trackW := ComputeTrackWidth(app.state.ViewportW)
	clickX := float64(trackW) * 0.5
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 6, Node: "trim_canvas", Kind: pluginapi.UIEventClick, Value: "450.0,26.0"}, nil)
	_ = clickX

	app.mu.Lock()
	defer app.mu.Unlock()
	if ms.StartSec != 20.5 {
		t.Fatalf("expected StartSec=20.5 after mark_in, got %v", ms.StartSec)
	}
	if ms.IsPlaying {
		t.Fatalf("expected step_fwd / trim_canvas click to stay paused")
	}
	if ms.PlayheadSec <= 20.5 {
		t.Fatalf("expected trim_canvas click to move playhead forward, got %v", ms.PlayheadSec)
	}
	if app.state.ConvertSubTab != "format" {
		t.Fatalf("expected ConvertSubTab=format, got %q", app.state.ConvertSubTab)
	}
}

func TestTimelineMath(t *testing.T) {
	ns, ne := ApplyHandleDrag("trim_start", 180, 0, 100, 100, 360)
	if ns <= 0 || ne != 100 {
		t.Fatalf("unexpected start drag result: ns=%v ne=%v", ns, ne)
	}
	ws, we := ApplyWindowDrag(100, 10, 30, 100, 400)
	if we-ws != 20 {
		t.Fatalf("expected preserved span 20s, got %v", we-ws)
	}
	pt := ApplyPlayheadDrag(200, 100, 400)
	if pt <= 49 || pt >= 53 {
		t.Fatalf("unexpected playhead drag result: %v", pt)
	}
	if got := FormatTimecodePrecise(65.4); got != "01:05.4" {
		t.Fatalf("FormatTimecodePrecise(65.4) = %q, want 01:05.4", got)
	}

	tlNode := BuildTimelineNode(600, 100, 10, 80, 40, "ff_trim_strip_1.jpg", []engine.ChapterInfo{{ID: 1, StartSec: 25}}, nil, true, false)
	if tlNode.ID != "trim_frame" || len(tlNode.Children) != 1 || tlNode.Children[0].ID != "trim_stack" {
		t.Fatalf("expected trim_frame wrapping trim_stack, got %q", tlNode.ID)
	}
	stackNode := tlNode.Children[0]
	canvasNode := stackNode.Children[0]
	cp, err := pluginapi.DecodeProps[pluginapi.UICanvasProps](canvasNode)
	if err != nil {
		t.Fatalf("decode trim_canvas props: %v", err)
	}
	if cp.BorderWidth != 0 || cp.Surface != "ground" {
		t.Fatalf("expected trim_canvas border_width=0 surface=ground inside trim_frame, got border=%d surface=%q", cp.BorderWidth, cp.Surface)
	}
	if cp.BindVideo != "video_player" || cp.BindDuration != 100 || cp.BindPos != 40 || cp.BindMin != 10 || cp.BindMax != 80 || cp.PlayheadWidth != 4 || !cp.Clickable {
		t.Fatalf("expected trim_canvas bound to video_player with BindMin=10 BindMax=80 PlayheadWidth=4, got %+v", cp)
	}
	if len(cp.Ops) == 0 || cp.Ops[0].Kind != "image" || cp.Ops[0].Fit != "fill" {
		t.Fatalf("expected first op to be full-bleed image with Fit=fill, got %+v", cp.Ops)
	}
	var scrimCount int
	for _, op := range cp.Ops {
		if op.Kind == "line" {
			t.Fatalf("expected vertical rect op instead of line for chapter markers")
		}
		if op.Token == "ink" {
			t.Fatalf("expected outside-range mask to use scrim instead of opaque ink")
		}
		if op.Token == "scrim" {
			scrimCount++
		}
	}
	if scrimCount != 2 {
		t.Fatalf("expected 2 scrim ops for trimmed [10,80] range, got %d", scrimCount)
	}
	winNode, hasWin := findNodeByID(stackNode, "trim_window")
	if !hasWin {
		t.Fatalf("expected trim_window on trimmed timeline")
	}
	wp, err := pluginapi.DecodeProps[pluginapi.UIDraggableProps](winNode)
	if err != nil || wp.Y != 0 || wp.Height != 10 {
		t.Fatalf("expected top-rail trim_window at Y=0 Height=10, got %+v err=%v", wp, err)
	}
	if _, hasPH := findNodeByID(stackNode, "playhead"); hasPH {
		t.Fatalf("expected separate playhead draggable removed in favor of trim_canvas scrubbing")
	}

	untrimmed := BuildTimelineNode(600, 100, 0, 100, 40, "", nil, nil, true, false)
	if _, hasWinUntrimmed := findNodeByID(untrimmed, "trim_window"); hasWinUntrimmed {
		t.Fatalf("expected untrimmed timeline to omit trim_window so full canvas scrubs freely")
	}
}

func findNodeByID(n pluginapi.UINode, id string) (pluginapi.UINode, bool) {
	if n.ID == id {
		return n, true
	}
	for _, ch := range n.Children {
		if found, ok := findNodeByID(ch, id); ok {
			return found, true
		}
	}
	return pluginapi.UINode{}, false
}

func TestStudioCanvasLayoutAndLoadingModal(t *testing.T) {
	app := NewApp(t.TempDir())
	defer app.Close()

	app.mu.Lock()
	app.state.HasFFmpeg = true
	app.state.Mode = engine.ModeTrim
	ms := app.state.SessionFor(engine.ModeTrim)
	ms.InputFiles = []string{"/tmp/demo.mp4"}
	ms.Probe = sampleVideoProbe()
	ms.PreviewLoaded = true
	ms.StartSec = 0
	ms.EndSec = 120
	ms.StartText = FormatTimestamp(0)
	ms.EndText = FormatTimestamp(120)
	ms.ProbeLoading = true
	ms.LoadingFile = "demo.mp4"
	ms.LoadingStage = "Inspecting media streams…"
	ms.LoadingPct = 25
	loadingState := app.state
	app.mu.Unlock()

	tr := RenderTree(loadingState)
	tr.Rev = 1
	if err := tr.Validate(); err != nil {
		t.Fatalf("loading modal tree invalid: %v", err)
	}
	modalNode, ok := findNodeByID(tr.Root, "loading_modal")
	if !ok {
		t.Fatalf("expected loading_modal when ProbeLoading is true")
	}
	dp, err := pluginapi.DecodeProps[pluginapi.UIDialogProps](modalNode)
	if err != nil || !dp.Blocking || !dp.Open {
		t.Fatalf("expected Blocking=true Open=true on loading_modal, got %+v err=%v", dp, err)
	}

	// Transport & shortcuts must be ignored while ProbeLoading; clearing ProbeLoading unlocks workspace
	// even if background FilmstripLoading is still active.
	ctx := context.Background()
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 1, Node: "btn_play_pause", Kind: pluginapi.UIEventClick}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 1, Node: "root", Kind: pluginapi.UIEventShortcut, Value: "Space"}, nil)
	app.mu.Lock()
	if ms.IsPlaying {
		t.Fatalf("expected playback blocked while ProbeLoading is true")
	}
	ms.ProbeLoading = false
	ms.FilmstripLoading = true
	if app.state.IsMediaLoading() {
		t.Fatalf("expected IsMediaLoading() false when only FilmstripLoading is true")
	}
	readyTreeWithBg := RenderTree(app.state)
	app.mu.Unlock()
	if _, ok := findNodeByID(readyTreeWithBg.Root, "loading_modal"); ok {
		t.Fatalf("expected no blocking loading_modal during background FilmstripLoading")
	}
	if _, ok := findNodeByID(readyTreeWithBg.Root, "tl_strip_loading"); !ok {
		t.Fatalf("expected subtle tl_strip_loading indicator while FilmstripLoading is true")
	}

	// Toggle VideoFit
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 3, Node: "btn_video_fit", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	readyState := app.state
	app.mu.Unlock()
	if readyState.EffectiveVideoFit() != "cover" {
		t.Fatalf("expected VideoFit=cover after btn_video_fit, got %q", readyState.EffectiveVideoFit())
	}

	readyTree := RenderTree(readyState)
	readyTree.Rev = 4
	if err := readyTree.Validate(); err != nil {
		t.Fatalf("ready tree invalid: %v", err)
	}
	topBar, ok := findNodeByID(readyTree.Root, "top_bar")
	if !ok {
		t.Fatalf("missing top_bar")
	}
	if _, ok := findNodeByID(topBar, "summary_title"); !ok {
		t.Fatalf("expected summary_title moved into top_bar")
	}
	stage, ok := findNodeByID(readyTree.Root, "monitor_stage")
	if !ok || len(stage.Children) != 1 || stage.Children[0].ID != "video_player" {
		t.Fatalf("expected monitor_stage to contain only video_player, got %+v", stage.Children)
	}
	vp, err := pluginapi.DecodeProps[pluginapi.UIVideoProps](stage.Children[0])
	if err != nil || !vp.Fill || !vp.Borderless || vp.Fit != "cover" {
		t.Fatalf("expected Fill=true Borderless=true Fit=cover on video_player, got %+v err=%v", vp, err)
	}
	deck, ok := findNodeByID(readyTree.Root, "timeline_deck")
	if !ok {
		t.Fatalf("missing timeline_deck")
	}
	for _, wantID := range []string{"btn_play_pause", "btn_fullscreen", "btn_video_fit", "vol_slider", "trim_frame"} {
		if _, ok := findNodeByID(deck, wantID); !ok {
			t.Fatalf("expected %q moved into timeline_deck", wantID)
		}
	}

	// Cancel loading clears state cleanly
	app.mu.Lock()
	ms.ProbeLoading = true
	app.mu.Unlock()
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 5, Node: "btn_cancel_load", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.state.IsMediaLoading() || ms.Probe != nil {
		t.Fatalf("expected cancel_load to clear loading and probe state")
	}
}

func TestModeSeparationDeckToggleAndPlayheadClamp(t *testing.T) {
	app := NewApp(t.TempDir())
	defer app.Close()
	ctx := context.Background()

	// 1. Empty mode omits timeline_deck and btn_top_deck_toggle
	emptyTree := RenderTree(app.state)
	emptyTree.Rev = 1
	if _, hasDeck := findNodeByID(emptyTree.Root, "timeline_deck"); hasDeck {
		t.Fatalf("expected empty state to omit timeline_deck")
	}
	if _, hasTopToggle := findNodeByID(emptyTree.Root, "btn_top_deck_toggle"); hasTopToggle {
		t.Fatalf("expected empty state to omit btn_top_deck_toggle")
	}

	// 2. Load Video A in Convert and Video B in Trim & Merge
	app.mu.Lock()
	app.state.HasFFmpeg = true
	conv := app.state.SessionFor(engine.ModeConvert)
	conv.InputFiles = []string{"/tmp/convert_a.mp4"}
	conv.Probe = sampleVideoProbe()
	conv.Probe.Path = "/tmp/convert_a.mp4"
	conv.PreviewLoaded = true
	conv.PlayheadSec = 12
	conv.IsPlaying = true

	trim := app.state.SessionFor(engine.ModeTrim)
	trim.InputFiles = []string{"/tmp/trim_b.mp4"}
	trim.Probe = sampleVideoProbe()
	trim.Probe.Path = "/tmp/trim_b.mp4"
	trim.PreviewLoaded = true
	trim.StartSec = 20
	trim.EndSec = 50
	trim.PlayheadSec = 30
	trim.StartText = FormatTimestamp(20)
	trim.EndText = FormatTimestamp(50)

	frames := app.state.SessionFor(engine.ModeFrames)
	frames.InputFiles = []string{"/tmp/frames_c.mp4"}
	frames.Probe = sampleVideoProbe()
	frames.Probe.Path = "/tmp/frames_c.mp4"
	frames.PreviewLoaded = true
	frames.StartSec = 10
	frames.EndSec = 40
	frames.PlayheadSec = 25
	app.mu.Unlock()

	// Convert renders playback_canvas only (no trim_start, trim_end, btn_add_clip)
	convTree := RenderTree(app.state)
	convTree.Rev = 2
	if err := convTree.Validate(); err != nil {
		t.Fatalf("convert tree invalid: %v", err)
	}
	if _, ok := findNodeByID(convTree.Root, "playback_canvas"); !ok {
		t.Fatalf("expected playback_canvas in convert mode")
	}
	for _, unexpected := range []string{"trim_canvas", "trim_start", "trim_end", "btn_add_clip"} {
		if _, ok := findNodeByID(convTree.Root, unexpected); ok {
			t.Fatalf("convert mode must not render %q", unexpected)
		}
	}

	// 3. Toggle deck collapse via btn_deck_toggle and expand via btn_top_deck_toggle
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 2, Node: "btn_deck_toggle", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	if !app.state.DeckCollapsed {
		app.mu.Unlock()
		t.Fatalf("expected DeckCollapsed=true after btn_deck_toggle")
	}
	collapsedTree := RenderTree(app.state)
	app.mu.Unlock()
	collapsedTree.Rev = 3
	if err := collapsedTree.Validate(); err != nil {
		t.Fatalf("collapsed tree invalid: %v", err)
	}
	if _, ok := findNodeByID(collapsedTree.Root, "playback_canvas"); ok {
		t.Fatalf("collapsed deck must hide playback_canvas")
	}
	if _, ok := findNodeByID(collapsedTree.Root, "btn_play_pause"); !ok {
		t.Fatalf("collapsed deck must keep btn_play_pause")
	}
	if _, ok := findNodeByID(collapsedTree.Root, "btn_deck_toggle"); !ok {
		t.Fatalf("collapsed deck must keep btn_deck_toggle")
	}

	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 3, Node: "btn_top_deck_toggle", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	if app.state.DeckCollapsed {
		app.mu.Unlock()
		t.Fatalf("expected DeckCollapsed=false after btn_top_deck_toggle")
	}
	app.mu.Unlock()

	// 4. Switch to Audio & Subs -> must be empty (no shared video from Convert) and Convert must pause
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 4, Node: "mode_tabs", Kind: pluginapi.UIEventSelect, Value: engine.ModeStreams}, nil)
	app.mu.Lock()
	if conv.IsPlaying {
		app.mu.Unlock()
		t.Fatalf("expected Convert playback to pause when switching modes")
	}
	if app.state.CurrentSession().Probe != nil {
		app.mu.Unlock()
		t.Fatalf("expected Audio & Subs to start empty without Convert's video")
	}
	streamsTree := RenderTree(app.state)
	app.mu.Unlock()
	if _, ok := findNodeByID(streamsTree.Root, "timeline_deck"); ok {
		t.Fatalf("expected empty Audio & Subs mode to omit timeline_deck")
	}

	// 5. Switch to GIF & Frames -> has trim_canvas without btn_add_clip
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 5, Node: "mode_tabs", Kind: pluginapi.UIEventSelect, Value: engine.ModeFrames}, nil)
	app.mu.Lock()
	framesTree := RenderTree(app.state)
	app.mu.Unlock()
	if _, ok := findNodeByID(framesTree.Root, "trim_canvas"); !ok {
		t.Fatalf("expected trim_canvas in GIF & Frames mode")
	}
	if _, ok := findNodeByID(framesTree.Root, "btn_add_clip"); ok {
		t.Fatalf("GIF & Frames mode must not render btn_add_clip")
	}

	// 6. Switch to Trim & Merge -> verify strict playhead clamping in [20, 50]
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 6, Node: "mode_tabs", Kind: pluginapi.UIEventSelect, Value: engine.ModeTrim}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 7, Node: "btn_skip_back", Kind: pluginapi.UIEventClick}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 8, Node: "btn_skip_back", Kind: pluginapi.UIEventClick}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 9, Node: "btn_skip_back", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	if trim.PlayheadSec != 20 {
		app.mu.Unlock()
		t.Fatalf("expected playhead clamped at StartSec=20, got %v", trim.PlayheadSec)
	}
	app.mu.Unlock()

	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 10, Node: "btn_jump_out", Kind: pluginapi.UIEventClick}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 11, Node: "btn_skip_fwd", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	if trim.PlayheadSec != 50 {
		app.mu.Unlock()
		t.Fatalf("expected playhead clamped at EndSec=50, got %v", trim.PlayheadSec)
	}
	// Clicking play at EndSec must replay from StartSec=20
	trim.PlayEnded = true
	app.mu.Unlock()
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 12, Node: "btn_play_pause", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	if !trim.IsPlaying || trim.PlayheadSec != 20 {
		app.mu.Unlock()
		t.Fatalf("expected Replay at EndSec to restart from StartSec=20, got playing=%v pos=%v", trim.IsPlaying, trim.PlayheadSec)
	}
	app.mu.Unlock()

	// 7. Queue is shared across modes
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 13, Node: "btn_add_queue", Kind: pluginapi.UIEventClick}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 14, Node: "mode_tabs", Kind: pluginapi.UIEventSelect, Value: engine.ModeConvert}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 15, Node: "btn_add_queue", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	if len(app.state.QueueItems) != 2 {
		app.mu.Unlock()
		t.Fatalf("expected shared queue with 2 items across modes, got %d", len(app.state.QueueItems))
	}
	app.mu.Unlock()
}

func TestProgressiveLoadSettingsAndHotReload(t *testing.T) {
	dataDir := t.TempDir()
	app := NewApp(dataDir)
	defer app.Close()
	ctx := context.Background()

	if _, err := app.Open(ctx, pluginapi.UIOpenParams{
		Tool: "ffmpeg",
		Settings: map[string]string{
			pluginapi.SettingPluginDataDir: dataDir,
		},
	}, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}

	// 1. Fresh defaults: video_preview=auto, timeline_filmstrip=true, preview_proxy=auto, hardware=auto/auto.
	app.mu.Lock()
	if app.state.VideoPreviewMode != VideoPreviewAuto || !app.state.TimelineFilmstrip || app.state.PreviewProxyPolicy != ProxyPolicyAuto || app.state.HWAccelMode != engine.HWAccelAuto || app.state.HWBackend != engine.HWBackendAuto {
		app.mu.Unlock()
		t.Fatalf("expected default auto/true/auto/auto/auto, got preview=%q strip=%v proxy=%q hw=%q/%q", app.state.VideoPreviewMode, app.state.TimelineFilmstrip, app.state.PreviewProxyPolicy, app.state.HWAccelMode, app.state.HWBackend)
	}
	for _, mode := range []string{engine.ModeConvert, engine.ModeTrim, engine.ModeStreams, engine.ModeFrames} {
		if !shouldAutoPreview(app.state.VideoPreviewMode, mode) {
			app.mu.Unlock()
			t.Fatalf("expected shouldAutoPreview=true for mode %q under default VideoPreviewAuto", mode)
		}
	}

	// 2. When set to trim_only: Convert starts deferred (stage_preview_deferred, no timeline_deck, no video_player).
	app.state.VideoPreviewMode = VideoPreviewTrimOnly
	app.state.HasFFmpeg = true
	app.state.Mode = engine.ModeConvert
	conv := app.state.SessionFor(engine.ModeConvert)
	conv.InputFiles = []string{"/tmp/demo.mp4"}
	conv.Probe = sampleVideoProbe()
	conv.PreviewLoaded = false
	deferredTree := RenderTree(app.state)
	app.mu.Unlock()
	deferredTree.Rev = 1
	if err := deferredTree.Validate(); err != nil {
		t.Fatalf("deferred tree invalid: %v", err)
	}
	if _, ok := findNodeByID(deferredTree.Root, "stage_preview_deferred"); !ok {
		t.Fatalf("expected stage_preview_deferred in Convert when PreviewLoaded=false")
	}
	if _, ok := findNodeByID(deferredTree.Root, "timeline_deck"); ok {
		t.Fatalf("expected timeline_deck omitted in Convert when PreviewLoaded=false")
	}
	if _, ok := findNodeByID(deferredTree.Root, "video_player"); ok {
		t.Fatalf("expected video_player omitted when PreviewLoaded=false")
	}

	// Clicking btn_load_preview mounts video_player and playback_canvas.
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 1, Node: "btn_load_preview", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	loadedTree := RenderTree(app.state)
	app.mu.Unlock()
	if _, ok := findNodeByID(loadedTree.Root, "video_player"); !ok {
		t.Fatalf("expected video_player after btn_load_preview")
	}
	if _, ok := findNodeByID(loadedTree.Root, "playback_canvas"); !ok {
		t.Fatalf("expected playback_canvas after btn_load_preview")
	}

	// 3. Non-blocking ProxyLoading shows stage_proxy_building + btn_cancel_proxy without loading_modal.
	app.mu.Lock()
	conv.ProxyLoading = true
	conv.ProxyStage = "Building 480p preview proxy…"
	proxyTree := RenderTree(app.state)
	app.mu.Unlock()
	proxyTree.Rev = 2
	if err := proxyTree.Validate(); err != nil {
		t.Fatalf("proxy building tree invalid: %v", err)
	}
	if _, ok := findNodeByID(proxyTree.Root, "loading_modal"); ok {
		t.Fatalf("ProxyLoading must not block workspace with loading_modal")
	}
	if _, ok := findNodeByID(proxyTree.Root, "stage_proxy_building"); !ok {
		t.Fatalf("expected stage_proxy_building when ProxyLoading=true")
	}
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 2, Node: "btn_cancel_proxy", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	if conv.ProxyLoading || !conv.ProxyRequired || conv.Probe == nil {
		app.mu.Unlock()
		t.Fatalf("expected btn_cancel_proxy to stop proxy without clearing Probe")
	}
	neededTree := RenderTree(app.state)
	app.mu.Unlock()
	if _, ok := findNodeByID(neededTree.Root, "stage_proxy_needed"); !ok {
		t.Fatalf("expected stage_proxy_needed after canceling proxy")
	}
	if _, ok := findNodeByID(neededTree.Root, "btn_build_proxy"); !ok {
		t.Fatalf("expected btn_build_proxy in stage_proxy_needed")
	}
	if _, ok := findNodeByID(neededTree.Root, "btn_try_native"); !ok {
		t.Fatalf("expected btn_try_native in stage_proxy_needed")
	}

	// 4. Hot-reload settings.json via UIEvent{Node: "settings", Kind: UIEventChange}:
	// Turn video_preview="off" and timeline_filmstrip=false, verify immediate reconciliation.
	settingsPath := filepath.Join(dataDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"video_preview":"off","timeline_filmstrip":false,"preview_proxy":"manual"}`), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	app.mu.Lock()
	trim := app.state.SessionFor(engine.ModeTrim)
	trim.InputFiles = []string{"/tmp/trim.mp4"}
	trim.Probe = sampleVideoProbe()
	trim.PreviewLoaded = true
	trim.StripAsset = "ff_trim_strip_1.jpg"
	trim.StartSec = 0
	trim.EndSec = 120
	app.state.Mode = engine.ModeTrim
	app.mu.Unlock()

	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 3, Node: "settings", Kind: pluginapi.UIEventChange}, nil)
	app.mu.Lock()
	if app.state.VideoPreviewMode != VideoPreviewOff || app.state.TimelineFilmstrip || app.state.PreviewProxyPolicy != ProxyPolicyManual {
		app.mu.Unlock()
		t.Fatalf("expected hot-reloaded settings, got preview=%q strip=%v proxy=%q", app.state.VideoPreviewMode, app.state.TimelineFilmstrip, app.state.PreviewProxyPolicy)
	}
	if trim.PreviewLoaded || trim.StripAsset != "" {
		app.mu.Unlock()
		t.Fatalf("expected reconciliation to unload preview and clear strip, got previewLoaded=%v strip=%q", trim.PreviewLoaded, trim.StripAsset)
	}
	offTree := RenderTree(app.state)
	app.mu.Unlock()
	offTree.Rev = 4
	if err := offTree.Validate(); err != nil {
		t.Fatalf("preview off tree invalid: %v", err)
	}
	if _, ok := findNodeByID(offTree.Root, "stage_preview_off"); !ok {
		t.Fatalf("expected stage_preview_off when video_preview=off")
	}
	if _, ok := findNodeByID(offTree.Root, "video_player"); ok {
		t.Fatalf("expected no video_player when video_preview=off")
	}
	if _, ok := findNodeByID(offTree.Root, "trim_start"); !ok {
		t.Fatalf("expected trim_start handles to remain available when video_preview=off")
	}

	// 5. Hot-reload back to automatic defaults ("auto" / true / "auto") and test hardware_accel hot-reload.
	if err := os.WriteFile(settingsPath, []byte(`{"video_preview":"auto","timeline_filmstrip":true,"preview_proxy":"auto","hardware_accel":"decode_only","hardware_backend":"videotoolbox"}`), 0o644); err != nil {
		t.Fatalf("write auto settings.json: %v", err)
	}
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 5, Node: "settings", Kind: pluginapi.UIEventChange}, nil)
	app.mu.Lock()
	if app.state.VideoPreviewMode != VideoPreviewAuto || !app.state.TimelineFilmstrip || app.state.PreviewProxyPolicy != ProxyPolicyAuto || app.state.HWAccelMode != engine.HWAccelDecodeOnly || app.state.HWBackend != engine.HWBackendVideoToolbox {
		app.mu.Unlock()
		t.Fatalf("expected auto/true/auto/decode_only/videotoolbox after hot-reload, got preview=%q strip=%v proxy=%q hw=%q/%q", app.state.VideoPreviewMode, app.state.TimelineFilmstrip, app.state.PreviewProxyPolicy, app.state.HWAccelMode, app.state.HWBackend)
	}
	if !trim.PreviewLoaded {
		app.mu.Unlock()
		t.Fatalf("expected PreviewLoaded=true after hot-reloading video_preview=auto")
	}

	// Switch to Convert Format subtab and verify conv_hw_status renders on hardware_accel="off".
	app.state.Mode = engine.ModeConvert
	app.state.ConvertSubTab = "format"
	app.mu.Unlock()
	if err := os.WriteFile(settingsPath, []byte(`{"hardware_accel":"off"}`), 0o644); err != nil {
		t.Fatalf("write hw off settings.json: %v", err)
	}
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 6, Node: "settings", Kind: pluginapi.UIEventChange}, nil)
	app.mu.Lock()
	hwOffTree := RenderTree(app.state)
	app.mu.Unlock()
	if _, ok := findNodeByID(hwOffTree.Root, "conv_hw_status"); !ok {
		t.Fatalf("expected conv_hw_status in Convert Format subtab")
	}
}
