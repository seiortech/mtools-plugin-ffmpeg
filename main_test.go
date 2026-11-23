package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/liveui"
	"github.com/seiortech/mtools-sdk/pluginapi"
)

func TestManifestAndSurfacesValid(t *testing.T) {
	mData, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var m pluginapi.Manifest
	if err := json.Unmarshal(mData, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	// Populate fields that mtools-plugin pack fills at signing time.
	m.Platform = "darwin-arm64"
	m.Publisher.PublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	m.Files = map[string]pluginapi.FileInfo{
		"bin/tool":      {SHA256: strings.Repeat("a", 64), Size: 1024, Executable: true},
		"surfaces.json": {SHA256: strings.Repeat("b", 64), Size: 1024, Executable: false},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest validate: %v", err)
	}

	sData, err := os.ReadFile("surfaces.json")
	if err != nil {
		t.Fatalf("read surfaces.json: %v", err)
	}
	doc, err := pluginapi.DecodeSurfaces(sData, m)
	if err != nil {
		t.Fatalf("DecodeSurfaces: %v", err)
	}
	if len(doc.Tools) != 1 || doc.Tools[0].Live == nil || !doc.Tools[0].Live.FullBleed {
		t.Fatalf("unexpected decoded surface tool: %+v", doc.Tools)
	}
}

func TestServeLiveRoundtrip(t *testing.T) {
	app := liveui.NewApp(t.TempDir())
	defer app.Close()
	runner := &ffRunner{dataDir: t.TempDir(), app: app}
	identity := pluginapi.InitializeResult{
		Protocol: pluginapi.Protocol,
		PluginID: "com.mtools.ffmpeg",
		Version:  "1.0.0",
	}

	inBuf := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol":"jsonrpc-stdio-v1","plugin_id":"com.mtools.ffmpeg","version":"1.0.0"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ui.open","params":{"tool":"ffmpeg","viewport":{"width":1024,"height":700}}}`,
		`{"jsonrpc":"2.0","method":"ui.event","params":{"rev":1,"node":"mode_trim","kind":"click"}}`,
		`{"jsonrpc":"2.0","method":"ui.close","params":{"tool":"ffmpeg"}}`,
		"",
	}, "\n")

	var outBuf bytes.Buffer
	if err := pluginapi.ServeLive(context.Background(), io.NopCloser(strings.NewReader(inBuf)), &outBuf, identity, runner, app); err != nil {
		t.Fatalf("ServeLive: %v", err)
	}

	out := outBuf.String()
	if !strings.Contains(out, `"plugin_id":"com.mtools.ffmpeg"`) {
		t.Fatalf("missing initialize response in %s", out)
	}
	if !strings.Contains(out, `"method":"ui.tree"`) {
		t.Fatalf("missing ui.tree update in %s", out)
	}
}
