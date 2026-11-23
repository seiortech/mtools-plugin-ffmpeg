package liveui

import "github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"

type Preset struct {
	ID          string
	Label       string
	Tag         string
	Description string
}

var ConvertPresets = []Preset{
	{
		ID:          "universal_mp4",
		Label:       "Universal MP4",
		Tag:         "H.264 · AAC · CRF 23",
		Description: "Best compatibility across phones, browsers, and editors.",
	},
	{
		ID:          "hevc_compress",
		Label:       "HEVC Small",
		Tag:         "H.265 · AAC · CRF 26",
		Description: "Shrinks file size by ~40% while keeping sharp detail.",
	},
	{
		ID:          "target_25mb",
		Label:       "Discord 25MB",
		Tag:         "H.264 · Target 24 MB",
		Description: "Auto-calculates bitrate to fit under 25 MB upload caps.",
	},
	{
		ID:          "fast_remux",
		Label:       "Fast Remux",
		Tag:         "MP4 · Stream Copy",
		Description: "Instant lossless container swap without re-encoding.",
	},
	{
		ID:          "audio_mp3",
		Label:       "Audio MP3",
		Tag:         "MP3 · 320 kbps",
		Description: "Extract high-bitrate stereo MP3 audio track.",
	},
	{
		ID:          "audio_flac",
		Label:       "Lossless FLAC",
		Tag:         "FLAC · Uncompressed",
		Description: "Extract bit-perfect archival audio with zero loss.",
	},
}

func ApplyConvertPreset(s *State, presetID string) {
	s.ActivePreset = presetID
	s.Opts.Preset = presetID
	switch presetID {
	case "universal_mp4":
		s.ConvertMediaKind = "video"
		s.Opts.Container = "mp4"
		s.Opts.VideoCodec = "libx264"
		s.Opts.AudioCodec = "aac"
		s.Opts.QualityMode = "crf"
		s.Opts.CRF = 23
		s.Opts.AudioBitrateK = 192
		s.Opts.EncoderPreset = "medium"
		s.Opts.ScaleHeight = 0
		s.Opts.MuteAudio = false
	case "hevc_compress":
		s.ConvertMediaKind = "video"
		s.Opts.Container = "mp4"
		s.Opts.VideoCodec = "libx265"
		s.Opts.AudioCodec = "aac"
		s.Opts.QualityMode = "crf"
		s.Opts.CRF = 26
		s.Opts.AudioBitrateK = 160
		s.Opts.EncoderPreset = "medium"
		s.Opts.ScaleHeight = 0
		s.Opts.MuteAudio = false
	case "target_25mb":
		s.ConvertMediaKind = "video"
		s.Opts.Container = "mp4"
		s.Opts.VideoCodec = "libx264"
		s.Opts.AudioCodec = "aac"
		s.Opts.QualityMode = "target_mb"
		s.Opts.TargetSizeMB = 24
		s.Opts.AudioBitrateK = 128
		s.Opts.EncoderPreset = "fast"
		s.Opts.MuteAudio = false
	case "fast_remux":
		s.ConvertMediaKind = "video"
		s.Opts.Container = "mp4"
		s.Opts.VideoCodec = "copy"
		s.Opts.AudioCodec = "copy"
		s.Opts.ScaleHeight = 0
		s.Opts.MuteAudio = false
	case "audio_mp3":
		s.ConvertMediaKind = "audio"
		s.Opts.Container = "mp3"
		s.Opts.AudioCodec = "libmp3lame"
		s.Opts.AudioBitrateK = 320
	case "audio_flac":
		s.ConvertMediaKind = "audio"
		s.Opts.Container = "flac"
		s.Opts.AudioCodec = "flac"
	}
	if s.Mode == "" {
		s.Mode = engine.ModeConvert
	}
}
