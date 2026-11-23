# mtools-plugin-ffmpeg

FFmpeg Studio Live UI sidecar plugin for **mtools** (`com.mtools.ffmpeg`).

## Features

- **Convert**: Presets (`Universal MP4`, `HEVC Small`, `Discord 25MB`, `Fast Remux`, `Audio MP3`, `Lossless FLAC`), container/codec selection, CRF quality, target max file size, scaling, and encoder preset.
- **Trim & Merge**: Interactive visual filmstrip timeline, IN/OUT grips, multi-clip segments, chapter splitting, lossless keyframe cut, and multi-file concatenation.
- **Audio & Subs**: Audio track selection, mute, external audio replacement, EBU R128 loudness normalization, soft-mux/hard-burn subtitles, and metadata editing/stripping.
- **GIF & Frames**: 2-pass `palettegen`/`paletteuse` GIF, animated WebP, interval frame sequence extraction, and thumbnail contact sheets.

## Build & Package

```sh
CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o dist/bin/tool .
go run github.com/seiortech/mtools-sdk/cmd/mtools-plugin@v0.2.5 pack \
  -manifest manifest.json \
  -surfaces surfaces.json \
  -bin dist/bin/tool \
  -key /path/to/key.priv \
  -out dist/com.mtools.ffmpeg.zip
go run github.com/seiortech/mtools-sdk/cmd/mtools-plugin@v0.2.5 verify \
  -bundle dist/com.mtools.ffmpeg.zip
```

## CI/CD & Releases

- `.github/workflows/ci.yml`: Runs `go vet ./...`, `go test -count=1 -race ./...`, and `CGO_ENABLED=0 go build` on pushes and pull requests to `master`.
- `.github/workflows/release.yml`: Triggers on `v*` tag pushes or manual `workflow_dispatch` (`publish_release` + `release_tag`). Requires the `MTOOLS_PLUGIN_PRIV_KEY` repository/organization secret, builds and signs bundles for `darwin-arm64`, `darwin-amd64`, `linux-amd64`, `linux-arm64`, `windows-amd64`, and `windows-arm64`, generates `SHA256SUMS.txt`, and publishes the GitHub Release.
