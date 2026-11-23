package liveui

import (
	"context"
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
	s.InputFiles = []string{"/tmp/demo.mp4", "/tmp/demo2.mkv"}
	s.Probe = sampleVideoProbe()
	s.PosterAsset = "ff_poster_1.jpg"
	s.StripAsset = "ff_strip_1.jpg"
	s.StartSec = 10
	s.EndSec = 45
	s.PlayheadSec = 22.5
	s.StartText = FormatTimestamp(10)
	s.EndText = FormatTimestamp(45)

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
	s.IsPlaying = true
	s.LoopSelection = true
	s.ProxyAsset = "ff_proxy_1.mp4"
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
	s.Probe = &engine.MediaProbe{
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
	s.Probe = sampleVideoProbe()
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
	app.state.InputFiles = []string{"/tmp/demo.mp4"}
	app.state.Probe = sampleVideoProbe()
	app.state.StartSec = 5
	app.state.EndSec = 60
	app.state.StartText = FormatTimestamp(5)
	app.state.EndText = FormatTimestamp(60)
	app.mu.Unlock()

	ctx := context.Background()
	// Play -> Pause -> Step -> Mark IN/OUT -> Timeline click -> Subtab switch
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 1, Node: "btn_play_pause", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	if !app.state.IsPlaying || app.state.PlaySeq != 1 {
		t.Fatalf("expected IsPlaying=true PlaySeq=1, got %v %d", app.state.IsPlaying, app.state.PlaySeq)
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
	if app.state.StartSec != 20.5 {
		t.Fatalf("expected StartSec=20.5 after mark_in, got %v", app.state.StartSec)
	}
	if app.state.IsPlaying {
		t.Fatalf("expected step_fwd / trim_canvas click to stay paused")
	}
	if app.state.PlayheadSec <= 1.0 {
		t.Fatalf("expected trim_canvas click to move playhead forward, got %v", app.state.PlayheadSec)
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

	tlNode := BuildTimelineNode(600, 100, 10, 80, 40, "", []engine.ChapterInfo{{ID: 1, StartSec: 25}}, nil, false)
	if tlNode.ID != "trim_frame" || len(tlNode.Children) != 1 || tlNode.Children[0].ID != "trim_stack" {
		t.Fatalf("expected trim_frame wrapping trim_stack, got %q", tlNode.ID)
	}
	stackNode := tlNode.Children[0]
	canvasNode := stackNode.Children[0]
	cp, err := pluginapi.DecodeProps[pluginapi.UICanvasProps](canvasNode)
	if err != nil {
		t.Fatalf("decode trim_canvas props: %v", err)
	}
	if cp.BorderWidth != 0 {
		t.Fatalf("expected trim_canvas border_width=0 inside trim_frame, got %d", cp.BorderWidth)
	}
	if cp.BindVideo != "video_player" || cp.BindDuration != 100 || cp.BindPos != 40 || cp.PlayheadWidth != 4 || !cp.Clickable {
		t.Fatalf("expected trim_canvas bound to video_player with PlayheadWidth=4, got %+v", cp)
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

	untrimmed := BuildTimelineNode(600, 100, 0, 100, 40, "", nil, nil, false)
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
	app.state.InputFiles = []string{"/tmp/demo.mp4"}
	app.state.Probe = sampleVideoProbe()
	app.state.StartSec = 0
	app.state.EndSec = 120
	app.state.StartText = FormatTimestamp(0)
	app.state.EndText = FormatTimestamp(120)
	app.state.ProbeLoading = true
	app.state.VideoLoadWaiting = true
	app.state.LoadingFile = "demo.mp4"
	app.state.LoadingStage = "Inspecting media streams…"
	app.state.LoadingPct = 15
	loadingState := app.state
	app.mu.Unlock()

	tr := RenderTree(loadingState)
	tr.Rev = 1
	if err := tr.Validate(); err != nil {
		t.Fatalf("loading modal tree invalid: %v", err)
	}
	modalNode, ok := findNodeByID(tr.Root, "loading_modal")
	if !ok {
		t.Fatalf("expected loading_modal when IsMediaLoading() is true")
	}
	dp, err := pluginapi.DecodeProps[pluginapi.UIDialogProps](modalNode)
	if err != nil || !dp.Blocking || !dp.Open {
		t.Fatalf("expected Blocking=true Open=true on loading_modal, got %+v err=%v", dp, err)
	}

	// Transport & shortcuts must be ignored while loading; ready=1 clears VideoLoadWaiting.
	ctx := context.Background()
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 1, Node: "btn_play_pause", Kind: pluginapi.UIEventClick}, nil)
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 1, Node: "root", Kind: pluginapi.UIEventShortcut, Value: "Space"}, nil)
	app.mu.Lock()
	if app.state.IsPlaying {
		t.Fatalf("expected playback blocked while IsMediaLoading() is true")
	}
	app.state.ProbeLoading = false
	app.mu.Unlock()

	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 2, Node: "video_player", Kind: pluginapi.UIEventMedia, Value: "pos=0.000|dur=120.000|playing=0|ended=0|ready=1|err="}, nil)
	app.mu.Lock()
	if app.state.IsMediaLoading() {
		t.Fatalf("expected IsMediaLoading() false after ready=1")
	}
	app.mu.Unlock()

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
	app.state.ProbeLoading = true
	app.mu.Unlock()
	_ = app.Event(ctx, pluginapi.UIEvent{Rev: 5, Node: "btn_cancel_load", Kind: pluginapi.UIEventClick}, nil)
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.state.IsMediaLoading() || app.state.Probe != nil {
		t.Fatalf("expected cancel_load to clear loading and probe state")
	}
}
