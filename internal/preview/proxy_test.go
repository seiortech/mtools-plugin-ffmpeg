package preview

import (
	"slices"
	"testing"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"
)

func TestNeedsPreviewProxy(t *testing.T) {
	cases := []struct {
		name  string
		probe *engine.MediaProbe
		want  bool
	}{
		{
			name: "native_mp4_h264",
			probe: &engine.MediaProbe{
				Path:         "/tmp/clip.mp4",
				VideoStreams: []engine.VideoStreamInfo{{Codec: "h264"}},
				AudioStreams: []engine.AudioStreamInfo{{Codec: "aac"}},
			},
			want: false,
		},
		{
			name: "mkv_h264",
			probe: &engine.MediaProbe{
				Path:         "/tmp/clip.mkv",
				VideoStreams: []engine.VideoStreamInfo{{Codec: "h264"}},
				AudioStreams: []engine.AudioStreamInfo{{Codec: "aac"}},
			},
			want: true,
		},
		{
			name: "webm_vp9",
			probe: &engine.MediaProbe{
				Path:         "/tmp/clip.webm",
				VideoStreams: []engine.VideoStreamInfo{{Codec: "vp9"}},
				AudioStreams: []engine.AudioStreamInfo{{Codec: "opus"}},
			},
			want: true,
		},
		{
			name: "flac_audio",
			probe: &engine.MediaProbe{
				Path:         "/tmp/song.flac",
				AudioStreams: []engine.AudioStreamInfo{{Codec: "flac"}},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		if got := NeedsPreviewProxy(tc.probe); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBuildProxyArgs(t *testing.T) {
	// MKV with H.264 + AAC should fast-remux with -c copy
	copyProbe := &engine.MediaProbe{
		Path:         "/tmp/clip.mkv",
		VideoStreams: []engine.VideoStreamInfo{{Codec: "h264"}},
		AudioStreams: []engine.AudioStreamInfo{{Codec: "aac"}},
	}
	copyArgs := BuildProxyArgs("/tmp/clip.mkv", copyProbe, "/tmp/out.mp4")
	if !slices.Contains(copyArgs, "copy") {
		t.Fatalf("expected fast -c copy remux for h264+aac mkv, got %v", copyArgs)
	}

	// MKV with H.264 + Opus should copy video (-c:v copy) and only transcode audio (-c:a aac)
	audioTranscodeProbe := &engine.MediaProbe{
		Path:         "/tmp/clip.mkv",
		VideoStreams: []engine.VideoStreamInfo{{Codec: "h264"}},
		AudioStreams: []engine.AudioStreamInfo{{Codec: "opus"}},
	}
	audioArgs := BuildProxyArgs("/tmp/clip.mkv", audioTranscodeProbe, "/tmp/out.mp4")
	if !slices.Contains(audioArgs, "-c:v") || !slices.Contains(audioArgs, "copy") || slices.Contains(audioArgs, "libx264") {
		t.Fatalf("expected -c:v copy without libx264 for h264+opus mkv, got %v", audioArgs)
	}

	// WebM VP9+Opus should transcode to ultrafast 480p libx264 with -threads 2
	vp9Probe := &engine.MediaProbe{
		Path:         "/tmp/clip.webm",
		VideoStreams: []engine.VideoStreamInfo{{Codec: "vp9"}},
		AudioStreams: []engine.AudioStreamInfo{{Codec: "opus"}},
	}
	vp9Args := BuildProxyArgs("/tmp/clip.webm", vp9Probe, "/tmp/out.mp4")
	if !slices.Contains(vp9Args, "libx264") || !slices.Contains(vp9Args, "ultrafast") || !slices.Contains(vp9Args, "-threads") || !slices.Contains(vp9Args, "2") || !slices.Contains(vp9Args, "scale=-2:min(ih\\,480)") {
		t.Fatalf("expected 480p libx264 ultrafast -threads 2 proxy for vp9, got %v", vp9Args)
	}

	// Audio-only FLAC should transcode audio only (-vn)
	flacProbe := &engine.MediaProbe{
		Path:         "/tmp/song.flac",
		AudioStreams: []engine.AudioStreamInfo{{Codec: "flac"}},
	}
	flacArgs := BuildProxyArgs("/tmp/song.flac", flacProbe, "/tmp/out.mp4")
	if !slices.Contains(flacArgs, "-vn") || !slices.Contains(flacArgs, "aac") {
		t.Fatalf("expected -vn aac proxy for flac, got %v", flacArgs)
	}
}
