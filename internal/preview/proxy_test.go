package preview

import (
	"runtime"
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

	// WebM VP9+Opus should transcode to ultrafast 480p libx264 with -threads 4
	vp9Probe := &engine.MediaProbe{
		Path:         "/tmp/clip.webm",
		VideoStreams: []engine.VideoStreamInfo{{Codec: "vp9"}},
		AudioStreams: []engine.AudioStreamInfo{{Codec: "opus"}},
	}
	vp9Args := BuildProxyArgs("/tmp/clip.webm", vp9Probe, "/tmp/out.mp4")
	if !slices.Contains(vp9Args, "libx264") || !slices.Contains(vp9Args, "ultrafast") || !slices.Contains(vp9Args, "-threads") || !slices.Contains(vp9Args, "4") || !slices.Contains(vp9Args, "scale=-2:min(ih\\,480)") {
		t.Fatalf("expected 480p libx264 ultrafast -threads 4 proxy for vp9, got %v", vp9Args)
	}
	if slices.Contains(vp9Args, "+faststart") {
		t.Fatalf("expected local proxy args to omit +faststart, got %v", vp9Args)
	}

	// Oversized H.264 MKV (>240 MiB) must transcode to 480p instead of stream-copying over the 256 MiB host asset limit
	bigMKV := &engine.MediaProbe{
		Path:         "/tmp/big.mkv",
		SizeBytes:    300 << 20,
		VideoStreams: []engine.VideoStreamInfo{{Codec: "h264"}},
		AudioStreams: []engine.AudioStreamInfo{{Codec: "aac"}},
	}
	bigArgs := BuildProxyArgs("/tmp/big.mkv", bigMKV, "/tmp/out.mp4")
	if !slices.Contains(bigArgs, "libx264") || slices.Contains(bigArgs, "copy") {
		t.Fatalf("expected >240 MiB MKV to transcode to 480p instead of stream-copying, got %v", bigArgs)
	}

	// Audio-only FLAC should transcode audio only (-vn)
	flacProbe := &engine.MediaProbe{
		Path:         "/tmp/song.flac",
		AudioStreams: []engine.AudioStreamInfo{{Codec: "flac"}},
	}
	flacArgs := BuildProxyArgs("/tmp/song.flac", flacProbe, "/tmp/out.mp4")
	if !slices.Contains(flacArgs, "-vn") || !slices.Contains(flacArgs, "aac") || slices.Contains(flacArgs, "+faststart") {
		t.Fatalf("expected -vn aac proxy without +faststart for flac, got %v", flacArgs)
	}
}

func TestCanFastRemuxProxyAndNoFaststart(t *testing.T) {
	smallMKV := &engine.MediaProbe{
		Path:         "/tmp/small.mkv",
		SizeBytes:    80 << 20,
		VideoStreams: []engine.VideoStreamInfo{{Codec: "h264"}},
	}
	if !CanFastRemuxProxy(smallMKV) {
		t.Fatalf("expected 80 MiB H.264 MKV to qualify for fast remux")
	}

	bigMKV := &engine.MediaProbe{
		Path:         "/tmp/big.mkv",
		SizeBytes:    300 << 20,
		VideoStreams: []engine.VideoStreamInfo{{Codec: "h264"}},
	}
	if CanFastRemuxProxy(bigMKV) {
		t.Fatalf("expected 300 MiB H.264 MKV to exceed MaxRemuxProxyBytes")
	}

	vp9WebM := &engine.MediaProbe{
		Path:         "/tmp/vp9.webm",
		SizeBytes:    20 << 20,
		VideoStreams: []engine.VideoStreamInfo{{Codec: "vp9"}},
	}
	if CanFastRemuxProxy(vp9WebM) {
		t.Fatalf("expected VP9 WebM not to qualify for fast remux")
	}

	flacAudio := &engine.MediaProbe{
		Path:         "/tmp/song.flac",
		SizeBytes:    30 << 20,
		AudioStreams: []engine.AudioStreamInfo{{Codec: "flac"}},
	}
	if !CanFastRemuxProxy(flacAudio) {
		t.Fatalf("expected small audio-only FLAC to qualify for fast proxy")
	}

	if runtime.GOOS == "darwin" {
		if _, ok := BuildHardwareProxyArgs("/tmp/small.mkv", smallMKV, "/tmp/out.mp4"); ok {
			t.Fatalf("expected BuildHardwareProxyArgs to skip when CanFastRemuxProxy is true")
		}
		hwArgs, ok := BuildHardwareProxyArgs("/tmp/big.mkv", bigMKV, "/tmp/out.mp4")
		if !ok {
			t.Fatalf("expected BuildHardwareProxyArgs to allow >240 MiB H.264 MKV on darwin")
		}
		if slices.Contains(hwArgs, "+faststart") {
			t.Fatalf("expected hardware proxy args to omit +faststart, got %v", hwArgs)
		}
	}
}

func TestBuildHardwareProxyArgsWithHW(t *testing.T) {
	vp9 := &engine.MediaProbe{
		Path:         "/tmp/vp9.webm",
		SizeBytes:    20 << 20,
		VideoStreams: []engine.VideoStreamInfo{{Codec: "vp9"}},
	}

	// 1. NVENC GPU decode + encode
	nvArgs, ok := BuildHardwareProxyArgsWithHW("/tmp/vp9.webm", vp9, engine.HWPlan{
		Mode:         engine.HWAccelAuto,
		Backend:      engine.HWBackendCUDA,
		DecodeMethod: "cuda",
		H264Encoder:  "h264_nvenc",
	}, "/tmp/out.mp4")
	if !ok || !slices.Contains(nvArgs, "-hwaccel") || !slices.Contains(nvArgs, "cuda") || !slices.Contains(nvArgs, "h264_nvenc") {
		t.Fatalf("expected cuda + h264_nvenc proxy args, got ok=%v args=%v", ok, nvArgs)
	}

	// 2. VA-API GPU decode + hwupload + h264_vaapi
	vaArgs, ok := BuildHardwareProxyArgsWithHW("/tmp/vp9.webm", vp9, engine.HWPlan{
		Mode:         engine.HWAccelAuto,
		Backend:      engine.HWBackendVAAPI,
		DecodeMethod: "vaapi",
		DecodeDevice: "/dev/dri/renderD128",
		H264Encoder:  "h264_vaapi",
	}, "/tmp/out.mp4")
	if !ok || !slices.Contains(vaArgs, "-vaapi_device") || !slices.Contains(vaArgs, "h264_vaapi") {
		t.Fatalf("expected vaapi proxy args, got ok=%v args=%v", ok, vaArgs)
	}

	// 3. DecodeOnly uses GPU decode + libx264
	decArgs, ok := BuildHardwareProxyArgsWithHW("/tmp/vp9.webm", vp9, engine.HWPlan{
		Mode:         engine.HWAccelDecodeOnly,
		Backend:      engine.HWBackendVideoToolbox,
		DecodeMethod: "videotoolbox",
	}, "/tmp/out.mp4")
	if !ok || !slices.Contains(decArgs, "videotoolbox") || !slices.Contains(decArgs, "libx264") {
		t.Fatalf("expected videotoolbox decode + libx264 in decode_only, got ok=%v args=%v", ok, decArgs)
	}

	// 4. Off returns false
	if _, ok := BuildHardwareProxyArgsWithHW("/tmp/vp9.webm", vp9, engine.HWPlan{Mode: engine.HWAccelOff}, "/tmp/out.mp4"); ok {
		t.Fatalf("expected HWAccelOff to return ok=false")
	}
}
