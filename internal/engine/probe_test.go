package engine

import "testing"

func TestParseFFprobeJSON(t *testing.T) {
	raw := []byte(`{
		"format": {"format_name": "mov,mp4,m4a,3gp,3g2,mj2", "duration": "125.500", "size": "52428800", "bit_rate": "3342000"},
		"streams": [
			{"index": 0, "codec_type": "video", "codec_name": "h264", "profile": "High", "pix_fmt": "yuv420p", "width": 1920, "height": 1080, "avg_frame_rate": "60000/1001", "bit_rate": "3100000"},
			{"index": 1, "codec_type": "audio", "codec_name": "aac", "sample_rate": "48000", "channels": 2, "channel_layout": "stereo", "bit_rate": "192000", "tags": {"language": "eng"}}
		],
		"chapters": [
			{"id": 0, "start_time": "0.0", "end_time": "45.0", "tags": {"title": "Intro"}}
		]
	}`)

	p, err := ParseFFprobeJSON(raw, "/tmp/demo.mp4", 52428800)
	if err != nil {
		t.Fatalf("ParseFFprobeJSON: %v", err)
	}
	if !p.HasVideo() || !p.HasAudio() {
		t.Fatalf("expected video and audio streams")
	}
	if p.VideoStreams[0].Width != 1920 || p.VideoStreams[0].Height != 1080 {
		t.Fatalf("unexpected video dims: %+v", p.VideoStreams[0])
	}
	if p.AudioStreams[0].Language != "eng" {
		t.Fatalf("expected eng audio language, got %q", p.AudioStreams[0].Language)
	}
	if len(p.Chapters) != 1 || p.Chapters[0].Title != "Intro" {
		t.Fatalf("unexpected chapters: %+v", p.Chapters)
	}
}

func TestParseFFmpegStderrProbe(t *testing.T) {
	stderr := `
Input #0, matroska,webm, from '/tmp/clip.mkv':
  Duration: 00:02:05.50, start: 0.000000, bitrate: 3342 kb/s
  Chapter #0:0: start 0.000000, end 45.000000
    Stream #0:0: Video: h264 (High), yuv420p(progressive), 1920x1080 [SAR 1:1 DAR 16:9], 30 fps, 30 tbr
    Stream #0:1(jpn): Audio: aac (LC), 48000 Hz, stereo, fltp
    Stream #0:2(eng): Subtitle: subrip
`
	p, err := ParseFFmpegStderrProbe(stderr, "/tmp/clip.mkv", 1024)
	if err != nil {
		t.Fatalf("ParseFFmpegStderrProbe: %v", err)
	}
	if p.DurationSec < 125 || p.DurationSec > 126 {
		t.Fatalf("unexpected duration: %v", p.DurationSec)
	}
	if len(p.VideoStreams) != 1 || p.VideoStreams[0].Width != 1920 || p.VideoStreams[0].FPS != 30 {
		t.Fatalf("unexpected video stream: %+v", p.VideoStreams)
	}
	if len(p.AudioStreams) != 1 || p.AudioStreams[0].Language != "jpn" {
		t.Fatalf("unexpected audio stream: %+v", p.AudioStreams)
	}
	if len(p.SubStreams) != 1 || p.SubStreams[0].Language != "eng" {
		t.Fatalf("unexpected sub stream: %+v", p.SubStreams)
	}
	if len(p.Chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d", len(p.Chapters))
	}
}
