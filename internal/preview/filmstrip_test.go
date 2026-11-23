package preview

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
	"testing"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"
)

func TestBuildFilmstripFilterUsesIncreaseCrop(t *testing.T) {
	vf := BuildFilmstripFilter(96, 54)
	if !strings.Contains(vf, "force_original_aspect_ratio=increase") || !strings.Contains(vf, "crop=96:54") || !strings.Contains(vf, "fast_bilinear") || !strings.Contains(vf, "setsar=1") {
		t.Fatalf("expected increase+fast_bilinear+crop+setsar filter, got %q", vf)
	}
	if strings.Contains(vf, "decrease") || strings.Contains(vf, "pad=") {
		t.Fatalf("unexpected letterbox/pad in filter %q", vf)
	}
}

func TestBuildSingleProcessFilmstripArgs(t *testing.T) {
	args := BuildSingleProcessFilmstripArgs("/tmp/demo.mp4", 10.0, 4, 96, 54, "/tmp/out.jpg")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-noaccurate_seek",
		"-an -sn -dn",
		"setpts=PTS-STARTPTS",
		"hstack=inputs=4:shortest=1",
		"flags=fast_bilinear",
		"setsar=1",
		"-update 1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in single-process args %q", want, joined)
		}
	}

	// Single tile must map [v0] directly without hstack=inputs=1.
	single := BuildSingleProcessFilmstripArgs("/tmp/demo.mp4", 0.02, 1, 96, 54, "/tmp/one.jpg")
	singleJoined := strings.Join(single, " ")
	if strings.Contains(singleJoined, "hstack") {
		t.Fatalf("expected single-tile args to omit hstack, got %q", singleJoined)
	}
	if !strings.Contains(singleJoined, "-map [v0]") {
		t.Fatalf("expected single-tile args to map [v0], got %q", singleJoined)
	}
}

func TestAssembleFilmstripNeighborFallbackAndAllNil(t *testing.T) {
	if _, ok := assembleFilmstrip([]image.Image{nil, nil, nil}, 10, 10); ok {
		t.Fatalf("expected ok=false when all tiles are nil")
	}

	tile := image.NewRGBA(image.Rect(0, 0, 10, 10))
	fillCol := color.RGBA{R: 74, G: 93, B: 69, A: 255}
	draw.Draw(tile, tile.Bounds(), &image.Uniform{C: fillCol}, image.Point{}, draw.Src)

	strip, ok := assembleFilmstrip([]image.Image{nil, tile, nil}, 10, 10)
	if !ok {
		t.Fatalf("expected ok=true when at least one tile is present")
	}
	if got := strip.Bounds().Dx(); got != 30 {
		t.Fatalf("expected strip width 30, got %d", got)
	}
	for _, x := range []int{5, 15, 25} {
		if c := strip.RGBAAt(x, 5); c != fillCol {
			t.Fatalf("expected neighbor-filled pixel at x=%d to equal %+v, got %+v", x, fillCol, c)
		}
	}
}

func TestBuildSingleProcessFilmstripArgsWithHW(t *testing.T) {
	hw := engine.HWPlan{Mode: engine.HWAccelAuto, Backend: engine.HWBackendVideoToolbox, DecodeMethod: "videotoolbox"}
	args := BuildSingleProcessFilmstripArgsWithHW("/tmp/demo.mp4", 10.0, 3, 96, 54, hw, "/tmp/out.jpg")
	joined := strings.Join(args, " ")
	if strings.Count(joined, "-hwaccel videotoolbox") != 3 {
		t.Fatalf("expected -hwaccel videotoolbox before each of 3 inputs, got %q", joined)
	}
	if strings.Contains(strings.Join(BuildSingleProcessFilmstripArgsWithHW("/tmp/demo.mp4", 10.0, 3, 96, 54, engine.HWPlan{Mode: engine.HWAccelOff}, "/tmp/out.jpg"), " "), "-hwaccel") {
		t.Fatalf("expected HWAccelOff to omit -hwaccel")
	}
}
