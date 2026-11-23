package liveui

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"
	"github.com/seiortech/mtools-sdk/pluginapi"
)

const (
	TimelineHeight = 52
	HandleWidth    = 10
	MinClipSeconds = 0.5
)

func ComputeTrackWidth(viewportW int) int {
	w := viewportW - 44
	if w < 220 {
		return 220
	}
	if w > 1500 {
		return 1500
	}
	return w
}

func TimeToX(sec, duration float64, trackW int) float64 {
	if duration <= 0 || trackW <= 0 {
		return 0
	}
	if sec < 0 {
		sec = 0
	}
	if sec > duration {
		sec = duration
	}
	return (sec / duration) * float64(trackW)
}

func XToTime(x, duration float64, trackW int) float64 {
	if duration <= 0 || trackW <= 0 {
		return 0
	}
	if x < 0 {
		x = 0
	}
	if x > float64(trackW) {
		x = float64(trackW)
	}
	return (x / float64(trackW)) * duration
}

func FormatTimestamp(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	total := int(math.Round(sec))
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func FormatTimecodePrecise(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	tenths := int(math.Round(sec*10)) % 10
	total := int(math.Floor(sec))
	if tenths == 0 && math.Round(sec*10) > float64(total*10) {
		total = int(math.Round(sec))
		tenths = 0
	}
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d.%d", h, m, s, tenths)
	}
	return fmt.Sprintf("%02d:%02d.%d", m, s, tenths)
}

func ParseTimestamp(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, false
	}
	var total float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			return 0, false
		}
		total = total*60 + v
	}
	return total, true
}

func ApplyHandleDrag(which string, xPx float64, startSec, endSec, duration float64, trackW int) (float64, float64) {
	if duration <= 0 {
		return startSec, endSec
	}
	minClip := math.Min(MinClipSeconds, duration)
	switch which {
	case "trim_start":
		t := XToTime(xPx+float64(HandleWidth)/2, duration, trackW)
		if t > endSec-minClip {
			t = endSec - minClip
		}
		if t < 0 {
			t = 0
		}
		return t, endSec
	case "trim_end":
		t := XToTime(xPx+float64(HandleWidth)/2, duration, trackW)
		if t < startSec+minClip {
			t = startSec + minClip
		}
		if t > duration {
			t = duration
		}
		return startSec, t
	default:
		return startSec, endSec
	}
}

func ApplyWindowDrag(xPx float64, startSec, endSec, duration float64, trackW int) (float64, float64) {
	if duration <= 0 {
		return startSec, endSec
	}
	span := endSec - startSec
	if span < 0 {
		span = 0
	}
	newStart := XToTime(xPx, duration, trackW)
	if newStart < 0 {
		newStart = 0
	}
	if newStart+span > duration {
		newStart = duration - span
	}
	if newStart < 0 {
		newStart = 0
	}
	return newStart, newStart + span
}

func ApplyPlayheadDrag(xPx, duration float64, trackW int) float64 {
	if duration <= 0 {
		return 0
	}
	return XToTime(xPx+float64(HandleWidth)/2, duration, trackW)
}

func BuildTimelineNode(
	trackW int,
	duration float64,
	startSec, endSec, playheadSec float64,
	stripAsset string,
	chapters []engine.ChapterInfo,
	clips []engine.ClipSegment,
	disabled bool,
) pluginapi.UINode {
	if trackW < 220 {
		trackW = 220
	}
	var startX, endX float64
	if duration <= 0 {
		startX = 0
		endX = float64(trackW)
	} else {
		startX = TimeToX(startSec, duration, trackW)
		endX = TimeToX(endSec, duration, trackW)
		if endX < startX+float64(HandleWidth) {
			endX = math.Min(float64(trackW), startX+float64(HandleWidth))
		}
	}

	ops := make([]pluginapi.UICanvasOp, 0, 16+len(chapters)+len(clips))
	if stripAsset != "" {
		ops = append(ops, pluginapi.UICanvasOp{
			Kind: "image",
			Src:  "asset:" + stripAsset,
			X:    0,
			Y:    0,
			W:    float64(trackW),
			H:    TimelineHeight,
		})
	} else {
		ops = append(ops, pluginapi.UICanvasOp{
			Kind:  "rect",
			Token: "field_support",
			X:     0,
			Y:     0,
			W:     float64(trackW),
			H:     TimelineHeight,
		})
	}

	for _, cl := range clips {
		cx1 := TimeToX(cl.StartSec, duration, trackW)
		cx2 := TimeToX(cl.EndSec, duration, trackW)
		if cx2 > cx1+2 {
			ops = append(ops, pluginapi.UICanvasOp{
				Kind:  "rect",
				Token: "blue_ink",
				X:     cx1,
				Y:     0,
				W:     cx2 - cx1,
				H:     5,
			})
		}
	}

	for _, ch := range chapters {
		if ch.StartSec <= 0 || ch.StartSec >= duration {
			continue
		}
		cx := TimeToX(ch.StartSec, duration, trackW)
		ops = append(ops, pluginapi.UICanvasOp{
			Kind:  "rect",
			Token: "paper",
			X:     math.Max(0, cx-1),
			Y:     3,
			W:     2,
			H:     TimelineHeight - 6,
		})
	}

	if startX > 1 {
		ops = append(ops, pluginapi.UICanvasOp{
			Kind:  "rect",
			Token: "scrim",
			X:     0,
			Y:     0,
			W:     startX,
			H:     TimelineHeight,
		})
	}
	if endX < float64(trackW)-1 {
		ops = append(ops, pluginapi.UICanvasOp{
			Kind:  "rect",
			Token: "scrim",
			X:     endX,
			Y:     0,
			W:     float64(trackW) - endX,
			H:     TimelineHeight,
		})
	}

	selW := math.Max(endX-startX, 4)
	ops = append(ops,
		pluginapi.UICanvasOp{Kind: "rect", Token: "accent", X: startX, Y: 0, W: selW, H: 3},
		pluginapi.UICanvasOp{Kind: "rect", Token: "accent", X: startX, Y: TimelineHeight - 3, W: selW, H: 3},
	)

	leftHandleX := math.Min(math.Max(startX-float64(HandleWidth)/2, 0), float64(trackW-HandleWidth))
	rightHandleX := math.Min(math.Max(endX-float64(HandleWidth)/2, 0), float64(trackW-HandleWidth))

	stackChildren := []pluginapi.UINode{
		{
			ID:   "trim_canvas",
			Kind: pluginapi.UINodeCanvas,
			Props: mustProps(pluginapi.UICanvasProps{
				Width:         trackW,
				Height:        TimelineHeight,
				BorderWidth:   0,
				Clickable:     !disabled && duration > 0,
				BindVideo:     "video_player",
				BindDuration:  duration,
				BindPos:       playheadSec,
				PlayheadWidth: 4,
				Ops:           ops,
			}),
		},
	}

	isTrimmed := !disabled && duration > 0 && (startSec > 0.05 || endSec < duration-0.05)
	if isTrimmed && endX-startX > float64(HandleWidth*2+12) {
		winX := startX + float64(HandleWidth)/2
		winW := int(endX - startX - float64(HandleWidth))
		stackChildren = append(stackChildren, pluginapi.UINode{
			ID:   "trim_window",
			Kind: pluginapi.UINodeDraggable,
			Props: mustProps(pluginapi.UIDraggableProps{
				X:         winX,
				Y:         0,
				Width:     winW,
				Height:    10,
				BoundW:    trackW,
				BoundH:    TimelineHeight,
				Resizable: false,
				Disabled:  false,
			}),
		})
	}

	stackChildren = append(stackChildren,
		pluginapi.UINode{
			ID:   "trim_start",
			Kind: pluginapi.UINodeDraggable,
			Props: mustProps(pluginapi.UIDraggableProps{
				X:         leftHandleX,
				Y:         0,
				Width:     HandleWidth,
				Height:    TimelineHeight,
				BoundW:    trackW,
				BoundH:    TimelineHeight,
				Resizable: false,
				Disabled:  disabled || duration <= 0,
			}),
			Children: []pluginapi.UINode{
				{
					ID:   "trim_start_bar",
					Kind: pluginapi.UINodeColumn,
					Props: mustProps(pluginapi.UIContainerProps{
						Width:   HandleWidth,
						Height:  TimelineHeight,
						Surface: "accent",
					}),
				},
			},
		},
		pluginapi.UINode{
			ID:   "trim_end",
			Kind: pluginapi.UINodeDraggable,
			Props: mustProps(pluginapi.UIDraggableProps{
				X:         rightHandleX,
				Y:         0,
				Width:     HandleWidth,
				Height:    TimelineHeight,
				BoundW:    trackW,
				BoundH:    TimelineHeight,
				Resizable: false,
				Disabled:  disabled || duration <= 0,
			}),
			Children: []pluginapi.UINode{
				{
					ID:   "trim_end_bar",
					Kind: pluginapi.UINodeColumn,
					Props: mustProps(pluginapi.UIContainerProps{
						Width:   HandleWidth,
						Height:  TimelineHeight,
						Surface: "accent",
					}),
				},
			},
		},
	)

	stack := pluginapi.UINode{
		ID:   "trim_stack",
		Kind: pluginapi.UINodeStack,
		Props: mustProps(pluginapi.UIContainerProps{
			Width:  trackW,
			Height: TimelineHeight,
		}),
		Children: stackChildren,
	}

	return pluginapi.UINode{
		ID:   "trim_frame",
		Kind: pluginapi.UINodeColumn,
		Props: mustProps(pluginapi.UIContainerProps{
			NoFillWidth: true,
			BorderSize:  2,
			BorderToken: "ink",
			Padding:     0,
		}),
		Children: []pluginapi.UINode{stack},
	}
}

func mustProps(v any) map[string]json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]json.RawMessage
	_ = json.Unmarshal(raw, &out)
	return out
}
