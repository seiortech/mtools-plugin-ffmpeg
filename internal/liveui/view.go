package liveui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"
	"github.com/seiortech/mtools-plugin-ffmpeg/internal/queue"
	"github.com/seiortech/mtools-sdk/pluginapi"
)

var mediaExtensions = []string{"mp4", "mkv", "mov", "webm", "avi", "m4v", "ts", "flv", "mp3", "m4a", "wav", "flac", "opus", "aac", "ogg"}

// RenderTree builds the Video Editor Studio Bauhaus × Sage Garden UITree for the current State.
func RenderTree(s State) pluginapi.UITree {
	rootChildren := make([]pluginapi.UINode, 0, 6)

	rootChildren = append(rootChildren, renderTopBar(s))

	if !s.HasFFmpeg {
		rootChildren = append(rootChildren, renderNeedBanner(
			"need_ffmpeg",
			"FFmpeg not found — required for probing, preview proxies, trimming, converting, and snapshots.",
			"Download FFmpeg",
			"ffmpeg_path",
		))
	}

	if s.ErrorMsg != "" {
		rootChildren = append(rootChildren, renderBanner("err_banner", s.ErrorMsg, "danger"))
	} else if s.ProbeError != "" {
		rootChildren = append(rootChildren, renderBanner("probe_err_banner", s.ProbeError, "danger"))
	} else if s.StatusMsg != "" {
		rootChildren = append(rootChildren, renderBanner("status_banner", s.StatusMsg, "field_support"))
	}

	splitW := s.RailWidth
	if splitW <= 0 {
		splitW = 300
	}
	if s.RailCollapsed {
		splitW = 48
	} else if s.ViewportW >= 680 {
		maxAllowed := float32(s.ViewportW) * 0.40
		if maxAllowed >= 250 && splitW > maxAllowed {
			splitW = maxAllowed
		}
	}

	rootChildren = append(rootChildren, pluginapi.UINode{
		ID:   "main_split",
		Kind: pluginapi.UINodeSplit,
		Props: mustProps(pluginapi.UISplitProps{
			Axis:               "h",
			Size:               splitW,
			Min:                250,
			Max:                420,
			DefaultSize:        300,
			Collapsible:        true,
			Collapsed:          s.RailCollapsed,
			ResetOnDoubleClick: true,
		}),
		Children: []pluginapi.UINode{
			renderMonitorStage(s),
			renderInspectorSidebar(s),
		},
	})

	rootChildren = append(rootChildren, renderTimelineDeck(s))

	if s.IsMediaLoading() {
		rootChildren = append(rootChildren, renderLoadingModal(s))
	} else if s.FullscreenOpen && s.Probe != nil && s.Probe.HasVideo() {
		rootChildren = append(rootChildren, renderFullscreenDialog(s))
	}

	return pluginapi.UITree{
		Root: pluginapi.UINode{
			ID:   "root",
			Kind: pluginapi.UINodeColumn,
			Props: mustProps(pluginapi.UIContainerProps{
				Surface:   "ground",
				Gap:       0,
				Fill:      true,
				Shortcuts: []string{"Space", "k", "j", "l", "i", "o", "Home", "End", ",", ".", "m", "s", "f", "Shift+l", "Escape"},
			}),
			Children: rootChildren,
		},
	}
}

func renderTopBar(s State) pluginapi.UINode {
	modeTabs := pluginapi.UINode{
		ID:   "mode_tabs",
		Kind: pluginapi.UINodeTabs,
		Props: mustProps(pluginapi.UITabsProps{
			Active:  nonEmptyStr(s.Mode, engine.ModeConvert),
			Compact: true,
			Tabs: []pluginapi.SurfaceOption{
				{Label: "Convert", Value: engine.ModeConvert},
				{Label: "Trim & Merge", Value: engine.ModeTrim},
				{Label: "Audio & Subs", Value: engine.ModeStreams},
				{Label: "GIF & Frames", Value: engine.ModeFrames},
			},
		}),
	}

	leftGroup := pluginapi.UINode{
		ID:   "topbar_left",
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Gap:         6,
			Align:       "center",
			NoFillWidth: true,
		}),
		Children: []pluginapi.UINode{modeTabs},
	}

	var titleKids []pluginapi.UINode
	if s.Probe != nil {
		metaLine := formatProbeMetaLine(s.Probe)
		titleKids = []pluginapi.UINode{
			{
				ID:   "summary_title",
				Kind: pluginapi.UINodeText,
				Props: mustProps(pluginapi.UITextProps{
					Text:       filepath.Base(s.Probe.Path),
					FontSize:   13,
					FontWeight: 700,
					Align:      "center",
					SingleLine: true,
					Tooltip:    s.Probe.Path + " · " + metaLine,
				}),
			},
			{
				ID:   "summary_meta",
				Kind: pluginapi.UINodeText,
				Props: mustProps(pluginapi.UITextProps{
					Text:        metaLine,
					Variant:     "muted",
					FontSize:    11,
					Align:       "center",
					TabularNums: true,
					SingleLine:  true,
				}),
			},
		}
	} else {
		titleKids = []pluginapi.UINode{
			{
				ID:   "summary_empty",
				Kind: pluginapi.UINodeText,
				Props: mustProps(pluginapi.UITextProps{
					Text:       "No media loaded",
					Variant:    "muted",
					FontSize:   12,
					Align:      "center",
					SingleLine: true,
				}),
			},
		}
	}
	titleGroup := pluginapi.UINode{
		ID:   "topbar_title",
		Kind: pluginapi.UINodeColumn,
		Props: mustProps(pluginapi.UIContainerProps{
			Gap:      1,
			PaddingH: 8,
			Align:    "center",
			Grow:     true,
		}),
		Children: titleKids,
	}

	hasInput := len(s.InputFiles) > 0 && s.HasFFmpeg
	toggleIcon := "collapse-right"
	if s.RailCollapsed {
		toggleIcon = "collapse-left"
	}

	rightControls := pluginapi.UINode{
		ID:   "topbar_right",
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Gap:         6,
			Align:       "center",
			NoFillWidth: true,
		}),
		Children: []pluginapi.UINode{
			{
				ID:   "btn_pick_media",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:    "Add Media",
					Icon:     "plus",
					Variant:  "secondary",
					Height:   30,
					PaddingX: 10,
					PaddingY: 4,
					PickFile: &pluginapi.UIFilePickerProps{
						Label:      "Select Media Files",
						Mode:       "open",
						Multiple:   true,
						Extensions: mediaExtensions,
					},
				}),
			},
			{
				ID:   "btn_add_queue",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:    "+ Queue",
					Variant:  "secondary",
					Height:   30,
					PaddingX: 10,
					PaddingY: 4,
					Disabled: !hasInput,
				}),
			},
			{
				ID:   "btn_export",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:    "Export",
					Variant:  "primary",
					Height:   30,
					PaddingX: 12,
					PaddingY: 4,
					Disabled: !hasInput,
				}),
			},
			{
				ID:   "btn_rail_toggle",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Icon:     toggleIcon,
					IconOnly: true,
					Variant:  "secondary",
					Width:    30,
					Height:   30,
					Tooltip:  "Toggle Inspector Sidebar",
				}),
			},
		},
	}

	return pluginapi.UINode{
		ID:   "top_bar",
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Surface:     "paper",
			BorderSides: [4]int{0, 0, 2, 0},
			BorderToken: "ink",
			PaddingH:    10,
			PaddingV:    6,
			Gap:         8,
			Align:       "center",
			Justify:     "between",
			Wrap:        true,
		}),
		Children: []pluginapi.UINode{leftGroup, titleGroup, rightControls},
	}
}

func formatProbeMetaLine(p *engine.MediaProbe) string {
	if p == nil {
		return ""
	}
	parts := []string{FormatTimestamp(p.DurationSec), formatBytesHuman(p.SizeBytes)}
	if p.HasVideo() {
		vs := p.VideoStreams[0]
		parts = append(parts, fmt.Sprintf("%dx%d %s", vs.Width, vs.Height, strings.ToUpper(vs.Codec)))
		if vs.FPS > 0 {
			parts = append(parts, fmt.Sprintf("%.0ffps", vs.FPS))
		}
	}
	if p.HasAudio() {
		as := p.AudioStreams[0]
		parts = append(parts, fmt.Sprintf("%s %dch", strings.ToUpper(as.Codec), as.Channels))
	}
	return strings.Join(parts, " · ")
}

func renderLoadingModal(s State) pluginapi.UINode {
	done := s.LoadingPct
	if done < 0 {
		done = 0
	} else if done > 100 {
		done = 100
	}
	stage := nonEmptyStr(s.LoadingStage, "Preparing media…")
	return pluginapi.UINode{
		ID:   "loading_modal",
		Kind: pluginapi.UINodeDialog,
		Props: mustProps(pluginapi.UIDialogProps{
			Title:       "Loading media…",
			Subtitle:    s.LoadingFile,
			Open:        true,
			Blocking:    true,
			Width:       420,
			BorderWidth: 3,
			Shadow:      [2]int{6, 6},
			Padding:     20,
		}),
		Children: []pluginapi.UINode{
			{
				ID:   "loading_prog",
				Kind: pluginapi.UINodeProgress,
				Props: mustProps(pluginapi.UIProgressProps{
					Done:  done,
					Total: 100,
					Label: stage,
				}),
			},
			{
				ID:   "loading_modal_acts",
				Kind: pluginapi.UINodeRow,
				Props: mustProps(pluginapi.UIContainerProps{
					Justify: "end",
					Align:   "center",
				}),
				Children: []pluginapi.UINode{
					{
						ID:   "btn_cancel_load",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Label:   "Cancel",
							Variant: "secondary",
						}),
					},
				},
			},
		},
	}
}

func renderNeedBanner(id, msg, btnLabel, settingKey string) pluginapi.UINode {
	return pluginapi.UINode{
		ID:   id,
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Surface:     "field_support",
			BorderSides: [4]int{0, 0, 2, 0},
			BorderToken: "ink",
			PaddingH:    14,
			PaddingV:    8,
			Gap:         12,
			Align:       "center",
			Justify:     "between",
		}),
		Children: []pluginapi.UINode{
			{
				ID:   id + "_txt",
				Kind: pluginapi.UINodeText,
				Props: mustProps(pluginapi.UITextProps{
					Text:    msg,
					Variant: "body",
					Grow:    true,
				}),
			},
			{
				ID:   id + "_btn",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:          btnLabel,
					Variant:        "primary",
					HostAction:     "run_app_setting_action",
					HostSettingKey: settingKey,
				}),
			},
		},
	}
}

func renderBanner(id, msg, surface string) pluginapi.UINode {
	variant := "body"
	if surface == "danger" {
		variant = "on_field"
	}
	return pluginapi.UINode{
		ID:   id,
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Surface:     surface,
			BorderSides: [4]int{0, 0, 2, 0},
			BorderToken: "ink",
			PaddingH:    14,
			PaddingV:    6,
		}),
		Children: []pluginapi.UINode{
			{
				ID:   id + "_txt",
				Kind: pluginapi.UINodeText,
				Props: mustProps(pluginapi.UITextProps{
					Text:    msg,
					Variant: variant,
				}),
			},
		},
	}
}

func renderMonitorStage(s State) pluginapi.UINode {
	stacked := s.ViewportW > 0 && s.ViewportW < 680

	if s.Probe == nil {
		availW := maxInt(260, s.ViewportW-344)
		if stacked {
			availW = maxInt(240, s.ViewportW-44)
		}
		emptyW := minInt(420, availW)
		return pluginapi.UINode{
			ID:   "monitor_stage",
			Kind: pluginapi.UINodeColumn,
			Props: mustProps(pluginapi.UIContainerProps{
				Surface:    "ground",
				Grow:       !stacked,
				FillHeight: !stacked,
				Padding:    14,
				Align:      "center",
				Justify:    "center",
			}),
			Children: []pluginapi.UINode{
				{
					ID:   "stage_empty",
					Kind: pluginapi.UINodeEmpty,
					Props: mustProps(pluginapi.UIEmptyProps{
						Title:       "FFmpeg Studio",
						Subtitle:    "Load a video or audio file to preview playback, set IN/OUT cuts on the filmstrip, and convert or remux locally.",
						Icon:        "film",
						IconSize:    36,
						ActionLabel: "Select Media Files…",
						Width:       emptyW,
						BorderWidth: 3,
						Shadow:      [2]int{5, 5},
						PickFile: &pluginapi.UIFilePickerProps{
							Label:      "Select Media Files",
							Mode:       "open",
							Multiple:   true,
							Extensions: mediaExtensions,
						},
					}),
				},
			},
		}
	}

	p := s.Probe
	if !p.HasVideo() {
		return pluginapi.UINode{
			ID:   "monitor_stage",
			Kind: pluginapi.UINodeColumn,
			Props: mustProps(pluginapi.UIContainerProps{
				Surface:    "ground",
				Grow:       !stacked,
				FillHeight: !stacked,
				Padding:    24,
				Align:      "center",
				Justify:    "center",
			}),
			Children: []pluginapi.UINode{renderAudioMonitorCard(s, 420)},
		}
	}

	var stageChild pluginapi.UINode
	switch {
	case s.FullscreenOpen:
		stageChild = pluginapi.UINode{
			ID:   "video_fs_placeholder",
			Kind: pluginapi.UINodeColumn,
			Props: mustProps(pluginapi.UIContainerProps{
				Fill:    !stacked,
				Height:  240,
				Surface: "ground",
				Align:   "center",
				Justify: "center",
			}),
			Children: []pluginapi.UINode{
				{
					ID:   "video_fs_ph_txt",
					Kind: pluginapi.UINodeText,
					Props: mustProps(pluginapi.UITextProps{
						Text:    "Playing in Fullscreen Monitor…",
						Variant: "muted",
					}),
				},
			},
		}
	case s.ProxyLoading && s.ProxyAsset == "":
		stageChild = pluginapi.UINode{
			ID:   "video_proxy_wait",
			Kind: pluginapi.UINodeColumn,
			Props: mustProps(pluginapi.UIContainerProps{
				Fill:    !stacked,
				Height:  240,
				Surface: "ground",
				Align:   "center",
				Justify: "center",
			}),
			Children: []pluginapi.UINode{
				{
					ID:   "video_proxy_wait_txt",
					Kind: pluginapi.UINodeText,
					Props: mustProps(pluginapi.UITextProps{
						Text:    "Preparing playable video preview…",
						Variant: "muted",
					}),
				},
			},
		}
	default:
		if stacked {
			vidH := minInt(360, maxInt(220, (s.ViewportW*9)/16))
			stageChild = buildVideoPlayerNode(s, 0, vidH, false)
		} else {
			stageChild = buildVideoPlayerNode(s, 0, 0, true)
		}
	}

	return pluginapi.UINode{
		ID:   "monitor_stage",
		Kind: pluginapi.UINodeColumn,
		Props: mustProps(pluginapi.UIContainerProps{
			Surface:    "ground",
			Grow:       !stacked,
			FillHeight: !stacked,
			Padding:    0,
			Gap:        0,
		}),
		Children: []pluginapi.UINode{stageChild},
	}
}

func buildVideoPlayerNode(s State, w, h int, fill bool) pluginapi.UINode {
	src := "file:" + s.Probe.Path
	if s.ProxyAsset != "" {
		src = "asset:" + s.ProxyAsset
	}
	vol := s.Volume
	rate := s.PlaybackRate
	if rate <= 0 {
		rate = 1.0
	}
	var loopStart, loopEnd float64
	if s.LoopSelection && s.EndSec > s.StartSec {
		loopStart = s.StartSec
		loopEnd = s.EndSec
	}
	poster := ""
	if s.PosterAsset != "" {
		poster = "asset:" + s.PosterAsset
	}

	return pluginapi.UINode{
		ID:   "video_player",
		Kind: pluginapi.UINodeVideo,
		Props: mustProps(pluginapi.UIVideoProps{
			Src:        src,
			Fit:        s.EffectiveVideoFit(),
			Surface:    "stage",
			Width:      w,
			Height:     h,
			Fill:       fill,
			Borderless: true,
			Playing:    s.IsPlaying,
			PlaySeq:    s.PlaySeq,
			SeekSec:    s.SeekTargetSec,
			SeekSeq:    s.SeekSeq,
			Rate:       rate,
			Volume:     &vol,
			Muted:      s.Muted,
			Loop:       s.LoopSelection,
			LoopStart:  loopStart,
			LoopEnd:    loopEnd,
			Poster:     poster,
		}),
	}
}

func renderAudioMonitorCard(s State, w int) pluginapi.UINode {
	clipDur := maxFloat(0, s.EndSec-s.StartSec)
	kids := []pluginapi.UINode{
		{
			ID:   "audio_card_title",
			Kind: pluginapi.UINodeText,
			Props: mustProps(pluginapi.UITextProps{
				Text:    "Audio Monitor",
				Variant: "heading",
			}),
		},
		{
			ID:   "audio_card_range",
			Kind: pluginapi.UINodeText,
			Props: mustProps(pluginapi.UITextProps{
				Text:        fmt.Sprintf("IN %s → OUT %s (Clip %s)", FormatTimestamp(s.StartSec), FormatTimestamp(s.EndSec), FormatTimestamp(clipDur)),
				Variant:     "muted",
				TabularNums: true,
			}),
		},
	}
	// Keep a 1px video_player node mounted so OS audio playback works for audio-only files.
	if s.Probe != nil && !s.FullscreenOpen {
		vp := buildVideoPlayerNode(s, 1, 1, false)
		kids = append(kids, vp)
	}
	return pluginapi.UINode{
		ID:   "audio_only_card",
		Kind: pluginapi.UINodeColumn,
		Props: mustProps(pluginapi.UIContainerProps{
			Surface: "ground",
			Border:  true,
			Padding: 20,
			Gap:     6,
			Width:   w,
			Height:  140,
			Align:   "center",
			Justify: "center",
		}),
		Children: kids,
	}
}

func renderFullscreenTransportRow(s State) pluginapi.UINode {
	dur := 0.0
	if s.Probe != nil {
		dur = s.Probe.DurationSec
	}
	playLabel, playIcon := playButtonState(s)
	return pluginapi.UINode{
		ID:   "fs_transport_row",
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Gap:     6,
			Align:   "center",
			Justify: "center",
			Wrap:    true,
		}),
		Children: []pluginapi.UINode{
			{
				ID:   "fs_jump_in",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Icon: "skip-back", IconOnly: true, Variant: "secondary", Width: 28, Height: 28,
					Tooltip: "Jump to IN / Start (Home)",
				}),
			},
			{
				ID:   "fs_skip_back",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label: "-5s", Variant: "secondary", Height: 28, PaddingX: 7, PaddingY: 3,
					Tooltip: "Back 5 seconds (J)",
				}),
			},
			{
				ID:   "fs_play_pause",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label: playLabel, Icon: playIcon, Variant: "primary", Height: 30, PaddingX: 12, PaddingY: 4,
					Tooltip: "Play / Pause / Resume (Space or K)",
				}),
			},
			{
				ID:   "fs_skip_fwd",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label: "+5s", Variant: "secondary", Height: 28, PaddingX: 7, PaddingY: 3,
					Tooltip: "Forward 5 seconds (L)",
				}),
			},
			{
				ID:   "fs_jump_out",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Icon: "skip-forward", IconOnly: true, Variant: "secondary", Width: 28, Height: 28,
					Tooltip: "Jump to OUT / End (End)",
				}),
			},
			{
				ID:   "fs_timecode",
				Kind: pluginapi.UINodeText,
				Props: mustProps(pluginapi.UITextProps{
					Text:          fmt.Sprintf("%s / %s", FormatTimecodePrecise(s.PlayheadSec), FormatTimecodePrecise(dur)),
					Variant:       "body",
					TabularNums:   true,
					FontWeight:    600,
					BindVideoTime: "video_player",
					BindDuration:  dur,
				}),
			},
			{
				ID:    "fs_mark_in",
				Kind:  pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{Label: "[ IN", Variant: "secondary"}),
			},
			{
				ID:    "fs_mark_out",
				Kind:  pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{Label: "OUT ]", Variant: "secondary"}),
			},
			{
				ID:    "fs_loop_toggle",
				Kind:  pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{Label: "Loop", Icon: "repeat", Variant: "secondary", Selected: s.LoopSelection}),
			},
			{
				ID:    "fs_snapshot_hd",
				Kind:  pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{Label: "HD", Icon: "camera", Variant: "secondary", Disabled: !s.HasFFmpeg}),
			},
		},
	}
}

func playButtonState(s State) (label, icon string) {
	switch {
	case s.IsPlaying:
		return "Pause", "pause"
	case s.PlayEnded:
		return "Replay", "play"
	case s.PlayheadSec > 0.15:
		return "Resume", "play"
	default:
		return "Play", "play"
	}
}

func renderFullscreenDialog(s State) pluginapi.UINode {
	w := s.ViewportW - 80
	if w < 480 {
		w = 480
	}
	h := s.ViewportH - 220
	if h < 260 {
		h = 260
	}
	return pluginapi.UINode{
		ID:   "fs_dialog",
		Kind: pluginapi.UINodeDialog,
		Props: mustProps(pluginapi.UIDialogProps{
			Title:       filepath.Base(s.Probe.Path),
			Subtitle:    "Press Space to Play/Pause · I/O to Mark IN/OUT · Esc to Exit Fullscreen",
			Open:        true,
			Width:       4096,
			MaxWidth:    4096,
			BorderWidth: 3,
			Shadow:      [2]int{6, 6},
			Padding:     14,
		}),
		Children: []pluginapi.UINode{
			pluginapi.UINode{
				ID:   "fs_stage_col",
				Kind: pluginapi.UINodeColumn,
				Props: mustProps(pluginapi.UIContainerProps{
					Gap:     12,
					Grow:    true,
					Align:   "center",
					Justify: "center",
				}),
				Children: []pluginapi.UINode{
					buildVideoPlayerNode(s, w, h, false),
					renderFullscreenTransportRow(s),
				},
			},
		},
	}
}

func renderInspectorSidebar(s State) pluginapi.UINode {
	if s.RailCollapsed {
		return pluginapi.UINode{
			ID:   "inspector_collapsed",
			Kind: pluginapi.UINodeColumn,
			Props: mustProps(pluginapi.UIContainerProps{
				Width:       48,
				FillHeight:  true,
				PaddingV:    12,
				Gap:         12,
				Align:       "center",
				Surface:     "paper",
				BorderSides: [4]int{0, 0, 0, 2},
				BorderToken: "ink",
			}),
			Children: []pluginapi.UINode{
				{
					ID:   "btn_rail_expand",
					Kind: pluginapi.UINodeButton,
					Props: mustProps(pluginapi.UIButtonProps{
						Icon:     "collapse-left",
						IconOnly: true,
						Variant:  "ghost",
						Width:    32,
						Height:   32,
						Tooltip:  "Expand Inspector",
					}),
				},
				{
					ID:   "rail_col_ic",
					Kind: pluginapi.UINodeIcon,
					Props: mustProps(pluginapi.UIIconProps{
						Name: "sliders",
						Size: 18,
						Tone: "accent",
					}),
				},
				{
					ID:   "rail_col_cnt",
					Kind: pluginapi.UINodeText,
					Props: mustProps(pluginapi.UITextProps{
						Text:        fmt.Sprintf("%d", len(s.QueueItems)),
						FontSize:    12,
						FontWeight:  700,
						TabularNums: true,
					}),
				},
			},
		}
	}

	activeTab := s.ActiveTab
	if activeTab != "queue" && activeTab != "files" {
		activeTab = "inspector"
	}

	tabsNode := pluginapi.UINode{
		ID:   "main_tabs",
		Kind: pluginapi.UINodeTabs,
		Props: mustProps(pluginapi.UITabsProps{
			Active:  activeTab,
			Compact: true,
			Tabs: []pluginapi.SurfaceOption{
				{Label: "Inspector", Value: "inspector"},
				{Label: fmt.Sprintf("Files (%d)", len(s.InputFiles)), Value: "files"},
				{Label: fmt.Sprintf("Queue (%d)", len(s.QueueItems)), Value: "queue"},
			},
		}),
	}

	children := []pluginapi.UINode{tabsNode}
	switch activeTab {
	case "queue":
		children = append(children, renderQueuePanel(s))
	case "files":
		children = append(children, renderFilesPanel(s))
	default:
		children = append(children, renderInspectorPanel(s))
	}

	stacked := s.ViewportW > 0 && s.ViewportW < 680
	sidebarKind := pluginapi.UINodeScroll
	borderSides := [4]int{0, 0, 0, 2}
	if stacked {
		sidebarKind = pluginapi.UINodeColumn
		borderSides = [4]int{2, 0, 0, 0}
	}

	return pluginapi.UINode{
		ID:   "inspector_sidebar",
		Kind: sidebarKind,
		Props: mustProps(pluginapi.UIContainerProps{
			FillHeight:  !stacked,
			Surface:     "paper",
			BorderSides: borderSides,
			BorderToken: "ink",
			Padding:     10,
			Gap:         10,
		}),
		Children: children,
	}
}

func renderFilesPanel(s State) pluginapi.UINode {
	hdr := pluginapi.UINode{
		ID:   "files_hdr_row",
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Gap:     6,
			Align:   "center",
			Justify: "between",
		}),
		Children: []pluginapi.UINode{
			{
				ID:   "files_add",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:   "+ Add Files",
					Variant: "secondary",
					PickFile: &pluginapi.UIFilePickerProps{
						Label:      "Add Media Files",
						Mode:       "open",
						Multiple:   true,
						Extensions: mediaExtensions,
					},
				}),
			},
			{
				ID:   "btn_clear_files",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:    "Clear",
					Variant:  "ghost",
					Disabled: len(s.InputFiles) == 0,
				}),
			},
		},
	}

	if len(s.InputFiles) == 0 {
		return pluginapi.UINode{
			ID:   "files_panel",
			Kind: pluginapi.UINodeColumn,
			Props: mustProps(pluginapi.UIContainerProps{Gap: 10}),
			Children: []pluginapi.UINode{
				hdr,
				{
					ID:   "files_empty",
					Kind: pluginapi.UINodeEmpty,
					Props: mustProps(pluginapi.UIEmptyProps{
						Title:    "No files loaded",
						Subtitle: "Add one or more videos or audio files to inspect, trim, or batch-merge.",
					}),
				},
			},
		}
	}

	kids := []pluginapi.UINode{hdr}
	for i, path := range s.InputFiles {
		selected := i == s.ActiveFileIdx
		surface := "ground"
		borderTok := "ink"
		if selected {
			surface = "field_support"
			borderTok = "accent"
		}
		kids = append(kids, pluginapi.UINode{
			ID:   fmt.Sprintf("file_row_%d", i),
			Kind: pluginapi.UINodeRow,
			Props: mustProps(pluginapi.UIContainerProps{
				Surface:     surface,
				Border:      true,
				BorderToken: borderTok,
				Padding:     6,
				Gap:         6,
				Align:       "center",
			}),
			Children: []pluginapi.UINode{
				{
					ID:   fmt.Sprintf("file_select_%d", i),
					Kind: pluginapi.UINodeButton,
					Props: mustProps(pluginapi.UIButtonProps{
						Label:    fmt.Sprintf("%d. %s", i+1, filepath.Base(path)),
						Variant:  "ghost",
						Selected: selected,
						Grow:     true,
						Tooltip:  path,
					}),
				},
				{
					ID:   fmt.Sprintf("file_remove_%d", i),
					Kind: pluginapi.UINodeButton,
					Props: mustProps(pluginapi.UIButtonProps{
						Icon:     "trash",
						IconOnly: true,
						Variant:  "danger",
						Width:    28,
						Height:   28,
					}),
				},
			},
		})
	}

	return pluginapi.UINode{
		ID:       "files_panel",
		Kind:     pluginapi.UINodeColumn,
		Props:    mustProps(pluginapi.UIContainerProps{Gap: 8}),
		Children: kids,
	}
}

func renderInspectorPanel(s State) pluginapi.UINode {
	var children []pluginapi.UINode

	switch s.Mode {
	case engine.ModeTrim:
		children = renderTrimInspector(s)
	case engine.ModeStreams:
		children = renderStreamsInspector(s)
	case engine.ModeFrames:
		children = renderFramesInspector(s)
	default:
		children = renderConvertInspector(s)
	}

	return pluginapi.UINode{
		ID:       "inspector_panel",
		Kind:     pluginapi.UINodeColumn,
		Props:    mustProps(pluginapi.UIContainerProps{Gap: 10}),
		Children: children,
	}
}

func renderConvertInspector(s State) []pluginapi.UINode {
	subTab := s.ConvertSubTab
	if subTab != "format" && subTab != "quality" && subTab != "output" {
		subTab = "presets"
	}

	nodes := []pluginapi.UINode{
		{
			ID:   "conv_subtabs",
			Kind: pluginapi.UINodeTabs,
			Props: mustProps(pluginapi.UITabsProps{
				Active:  subTab,
				Compact: true,
				Tabs: []pluginapi.SurfaceOption{
					{Label: "Presets", Value: "presets"},
					{Label: "Format", Value: "format"},
					{Label: "Quality", Value: "quality"},
					{Label: "Output", Value: "output"},
				},
			}),
		},
	}

	switch subTab {
	case "presets":
		for _, p := range ConvertPresets {
			selected := s.ActivePreset == p.ID
			surface := "ground"
			borderTok := "ink"
			if selected {
				surface = "field_support"
				borderTok = "accent"
			}
			nodes = append(nodes, pluginapi.UINode{
				ID:   "preset_card_" + p.ID,
				Kind: pluginapi.UINodeColumn,
				Props: mustProps(pluginapi.UIContainerProps{
					Surface:     surface,
					Border:      true,
					BorderToken: borderTok,
					Padding:     8,
					Gap:         4,
				}),
				Children: []pluginapi.UINode{
					{
						ID:   "preset_hdr_" + p.ID,
						Kind: pluginapi.UINodeRow,
						Props: mustProps(pluginapi.UIContainerProps{
							Justify: "between",
							Align:   "center",
							Gap:     6,
						}),
						Children: []pluginapi.UINode{
							{
								ID:   "preset_" + p.ID,
								Kind: pluginapi.UINodeButton,
								Props: mustProps(pluginapi.UIButtonProps{
									Label:    p.Label,
									Variant:  "secondary",
									Selected: selected,
								}),
							},
							{
								ID:   "preset_tag_" + p.ID,
								Kind: pluginapi.UINodeText,
								Props: mustProps(pluginapi.UITextProps{
									Text:     p.Tag,
									Variant:  "muted",
									FontSize: 11,
								}),
							},
						},
					},
					{
						ID:   "preset_desc_" + p.ID,
						Kind: pluginapi.UINodeText,
						Props: mustProps(pluginapi.UITextProps{
							Text:     p.Description,
							Variant:  "muted",
							FontSize: 12,
						}),
					},
				},
			})
		}

	case "format":
		mediaKind := nonEmptyStr(s.ConvertMediaKind, "video")
		if engine.IsAudioOnlyContainer(s.Opts.Container) {
			mediaKind = "audio"
		}
		nodes = append(nodes, pluginapi.UINode{
			ID:   "conv_media_kind",
			Kind: pluginapi.UINodeSelect,
			Props: mustProps(pluginapi.UISelectProps{
				Label: "Output Type",
				Value: mediaKind,
				Style: "chips",
				Options: []pluginapi.SurfaceOption{
					{Label: "Video + Audio", Value: "video"},
					{Label: "Audio Only", Value: "audio"},
				},
			}),
		})

		containerOpts := []pluginapi.SurfaceOption{
			{Label: "MP4 (Universal)", Value: "mp4"},
			{Label: "MKV (Matroska)", Value: "mkv"},
			{Label: "WebM (Web)", Value: "webm"},
			{Label: "MOV (QuickTime)", Value: "mov"},
		}
		if mediaKind == "audio" {
			containerOpts = []pluginapi.SurfaceOption{
				{Label: "MP3 (Audio)", Value: "mp3"},
				{Label: "M4A (AAC Audio)", Value: "m4a"},
				{Label: "FLAC (Lossless)", Value: "flac"},
				{Label: "WAV (PCM Audio)", Value: "wav"},
				{Label: "Opus (Compact)", Value: "opus"},
			}
		}

		nodes = append(nodes, pluginapi.UINode{
			ID:   "conv_container",
			Kind: pluginapi.UINodeSelect,
			Props: mustProps(pluginapi.UISelectProps{
				Label:   "Container",
				Value:   s.Opts.Container,
				Style:   "dropdown",
				Options: containerOpts,
			}),
		})

		if mediaKind == "video" {
			nodes = append(nodes, pluginapi.UINode{
				ID:   "conv_vcodec",
				Kind: pluginapi.UINodeSelect,
				Props: mustProps(pluginapi.UISelectProps{
					Label: "Video Codec",
					Value: s.Opts.VideoCodec,
					Style: "dropdown",
					Options: []pluginapi.SurfaceOption{
						{Label: "H.264 (libx264)", Value: "libx264"},
						{Label: "H.265 / HEVC (libx265)", Value: "libx265"},
						{Label: "AV1 (libsvtav1)", Value: "libsvtav1"},
						{Label: "VP9 (libvpx-vp9)", Value: "libvpx-vp9"},
						{Label: "ProRes (prores_ks)", Value: "prores_ks"},
						{Label: "Stream Copy (No Re-encode)", Value: "copy"},
					},
				}),
			})
			if warn, ok := engine.ValidateCodecContainer(s.Opts.Container, s.Opts.VideoCodec, s.Opts.AudioCodec); !ok {
				nodes = append(nodes, pluginapi.UINode{
					ID:   "conv_compat_warn",
					Kind: pluginapi.UINodeText,
					Props: mustProps(pluginapi.UITextProps{
						Text:    warn,
						Variant: "danger",
					}),
				})
			}
		}

		nodes = append(nodes,
			pluginapi.UINode{
				ID:   "conv_acodec",
				Kind: pluginapi.UINodeSelect,
				Props: mustProps(pluginapi.UISelectProps{
					Label: "Audio Codec",
					Value: nonEmptyStr(s.Opts.AudioCodec, "aac"),
					Style: "dropdown",
					Options: []pluginapi.SurfaceOption{
						{Label: "AAC (Standard)", Value: "aac"},
						{Label: "MP3 (libmp3lame)", Value: "libmp3lame"},
						{Label: "Opus (libopus)", Value: "libopus"},
						{Label: "FLAC (Lossless)", Value: "flac"},
						{Label: "Stream Copy", Value: "copy"},
					},
				}),
			},
			pluginapi.UINode{
				ID:   "conv_abitrate",
				Kind: pluginapi.UINodeSelect,
				Props: mustProps(pluginapi.UISelectProps{
					Label: "Audio Bitrate",
					Value: strconv.Itoa(clampIntDefault(s.Opts.AudioBitrateK, 192)),
					Style: "dropdown",
					Options: []pluginapi.SurfaceOption{
						{Label: "128 kbps (Compact)", Value: "128"},
						{Label: "192 kbps (Balanced)", Value: "192"},
						{Label: "256 kbps (High)", Value: "256"},
						{Label: "320 kbps (Studio)", Value: "320"},
					},
				}),
			},
		)

	case "quality":
		audioOnly := engine.IsAudioOnlyContainer(s.Opts.Container)
		isCopy := s.Opts.VideoCodec == "copy"
		if audioOnly || isCopy {
			msg := "Audio-only container selected — adjust audio bitrate in the Format tab."
			if isCopy && !audioOnly {
				msg = "Stream Copy mode copies video frames losslessly without re-encoding."
			}
			nodes = append(nodes, pluginapi.UINode{
				ID:   "qual_copy_note",
				Kind: pluginapi.UINodeText,
				Props: mustProps(pluginapi.UITextProps{
					Text:    msg,
					Variant: "muted",
				}),
			})
		} else {
			nodes = append(nodes, pluginapi.UINode{
				ID:   "conv_quality_mode",
				Kind: pluginapi.UINodeSelect,
				Props: mustProps(pluginapi.UISelectProps{
					Label: "Rate Control",
					Value: s.Opts.QualityMode,
					Style: "dropdown",
					Options: []pluginapi.SurfaceOption{
						{Label: "Constant Quality (CRF)", Value: "crf"},
						{Label: "Target Max Size (MB)", Value: "target_mb"},
						{Label: "Target Bitrate (kbps)", Value: "bitrate"},
					},
				}),
			})
			switch s.Opts.QualityMode {
			case "target_mb":
				nodes = append(nodes, pluginapi.UINode{
					ID:   "conv_target_mb",
					Kind: pluginapi.UINodeSelect,
					Props: mustProps(pluginapi.UISelectProps{
						Label: "Target Max File Size",
						Value: fmt.Sprintf("%.0f", s.Opts.TargetSizeMB),
						Style: "dropdown",
						Options: []pluginapi.SurfaceOption{
							{Label: "10 MB (Web / Email)", Value: "10"},
							{Label: "24 MB (Discord Free)", Value: "24"},
							{Label: "50 MB (Discord Nitro)", Value: "50"},
							{Label: "100 MB", Value: "100"},
						},
					}),
				})
			case "bitrate":
				nodes = append(nodes, pluginapi.UINode{
					ID:   "conv_vbitrate",
					Kind: pluginapi.UINodeSelect,
					Props: mustProps(pluginapi.UISelectProps{
						Label: "Video Bitrate",
						Value: strconv.Itoa(s.Opts.VideoBitrateK),
						Style: "dropdown",
						Options: []pluginapi.SurfaceOption{
							{Label: "1500 kbps", Value: "1500"},
							{Label: "2500 kbps", Value: "2500"},
							{Label: "5000 kbps", Value: "5000"},
							{Label: "10000 kbps", Value: "10000"},
						},
					}),
				})
			default:
				crf := float64(s.Opts.CRF)
				if crf < 14 || crf > 36 {
					crf = 23
				}
				nodes = append(nodes,
					pluginapi.UINode{
						ID:   "conv_crf",
						Kind: pluginapi.UINodeSlider,
						Props: mustProps(pluginapi.UISliderProps{
							Label:       "CRF Quality (lower = sharper)",
							Min:         14,
							Max:         36,
							Step:        1,
							Value:       crf,
							ValueSuffix: " CRF",
						}),
					},
					pluginapi.UINode{
						ID:   "conv_crf_hint",
						Kind: pluginapi.UINodeText,
						Props: mustProps(pluginapi.UITextProps{
							Text:     crfQualityHint(int(crf)),
							Variant:  "muted",
							FontSize: 12,
						}),
					},
				)
			}

			nodes = append(nodes,
				pluginapi.UINode{
					ID:   "conv_scale",
					Kind: pluginapi.UINodeSelect,
					Props: mustProps(pluginapi.UISelectProps{
						Label: "Resolution Scale",
						Value: strconv.Itoa(s.Opts.ScaleHeight),
						Style: "dropdown",
						Options: []pluginapi.SurfaceOption{
							{Label: "Original Resolution", Value: "0"},
							{Label: "4K (2160p)", Value: "2160"},
							{Label: "1080p (Full HD)", Value: "1080"},
							{Label: "720p (HD)", Value: "720"},
							{Label: "480p (SD)", Value: "480"},
						},
					}),
				},
				pluginapi.UINode{
					ID:   "conv_preset",
					Kind: pluginapi.UINodeSelect,
					Props: mustProps(pluginapi.UISelectProps{
						Label: "Encoder Speed",
						Value: s.Opts.EncoderPreset,
						Style: "dropdown",
						Options: []pluginapi.SurfaceOption{
							{Label: "Ultrafast", Value: "ultrafast"},
							{Label: "Fast", Value: "fast"},
							{Label: "Medium (Balanced)", Value: "medium"},
							{Label: "Slow (Better Compression)", Value: "slow"},
						},
					}),
				},
			)
		}

	case "output":
		nodes = append(nodes, renderOutputSection(s)...)
	}

	return nodes
}

func renderOutputSection(s State) []pluginapi.UINode {
	nodes := []pluginapi.UINode{
		{
			ID:   "output_dir_picker",
			Kind: pluginapi.UINodeDirPicker,
			Props: mustProps(pluginapi.UIDirPickerProps{
				Label: "Output Folder",
				Value: s.OutputDir,
			}),
		},
	}

	if s.Probe != nil {
		clipDur := maxFloat(0, s.EndSec-s.StartSec)
		if clipDur <= 0 {
			clipDur = s.Probe.DurationSec
		}
		estBytes, compatLabel, compatOK := engine.EstimateOutputBytes(s.Probe, s.Mode, s.Opts, clipDur)
		estLine := compatLabel
		if estBytes > 0 {
			estLine = fmt.Sprintf("Est. %s · %s", formatBytesHuman(estBytes), compatLabel)
		}
		estVariant := "body"
		if !compatOK {
			estVariant = "danger"
		}
		nodes = append(nodes, pluginapi.UINode{
			ID:   "output_est_card",
			Kind: pluginapi.UINodeColumn,
			Props: mustProps(pluginapi.UIContainerProps{
				Surface: "ground",
				Border:  true,
				Padding: 10,
				Gap:     4,
			}),
			Children: []pluginapi.UINode{
				{
					ID:   "output_est_hdr",
					Kind: pluginapi.UINodeText,
					Props: mustProps(pluginapi.UITextProps{
						Text:       "Export Estimate",
						FontSize:   12,
						FontWeight: 700,
					}),
				},
				{
					ID:   "output_est_val",
					Kind: pluginapi.UINodeText,
					Props: mustProps(pluginapi.UITextProps{
						Text:        estLine,
						Variant:     estVariant,
						TabularNums: true,
					}),
				},
			},
		})
	}

	cmdToggleLabel := "Show FFmpeg Command ▾"
	if s.ShowCmdPreview {
		cmdToggleLabel = "Hide FFmpeg Command ▴"
	}
	nodes = append(nodes, pluginapi.UINode{
		ID:   "btn_toggle_cmd",
		Kind: pluginapi.UINodeButton,
		Props: mustProps(pluginapi.UIButtonProps{
			Label:     cmdToggleLabel,
			Variant:   "ghost",
			FillWidth: true,
		}),
	})
	if s.ShowCmdPreview && s.Probe != nil {
		if spec, err := s.buildCurrentJobSpec(); err == nil {
			if plan, perr := engine.BuildCommandPlan(spec, s.Probe); perr == nil {
				nodes = append(nodes, pluginapi.UINode{
					ID:   "cmd_preview_box",
					Kind: pluginapi.UINodeColumn,
					Props: mustProps(pluginapi.UIContainerProps{
						Surface: "ground",
						Border:  true,
						Padding: 8,
					}),
					Children: []pluginapi.UINode{
						{
							ID:   "cmd_preview_txt",
							Kind: pluginapi.UINodeText,
							Props: mustProps(pluginapi.UITextProps{
								Text:     engine.FormatCommandPreview(s.FFmpegPath, plan),
								FontSize: 12,
							}),
						},
					},
				})
			}
		}
	}
	return nodes
}

func renderTrimInspector(s State) []pluginapi.UINode {
	concatOpts := []pluginapi.SurfaceOption{
		{Label: "Single Clip / Cut List", Value: "single"},
	}
	if len(s.InputFiles) >= 2 {
		concatOpts = append(concatOpts, pluginapi.SurfaceOption{
			Label: fmt.Sprintf("Merge %d Queued Files", len(s.InputFiles)),
			Value: "concat_files",
		})
	}

	nodes := []pluginapi.UINode{
		{
			ID:    "sec_trim_lbl",
			Kind:  pluginapi.UINodeText,
			Props: mustProps(pluginapi.UITextProps{Text: "Trim & Merge Options", Variant: "heading"}),
		},
		{
			ID:   "trim_lossless",
			Kind: pluginapi.UINodeToggle,
			Props: mustProps(pluginapi.UIToggleProps{
				Label:   "Lossless Keyframe Cut (-c copy)",
				Checked: s.Opts.LosslessCut,
			}),
		},
		{
			ID:   "trim_concat_mode",
			Kind: pluginapi.UINodeSelect,
			Props: mustProps(pluginapi.UISelectProps{
				Label:   "Export Behavior",
				Value:   nonEmptyStr(s.Opts.ConcatMode, "single"),
				Style:   "dropdown",
				Options: concatOpts,
			}),
		},
		{
			ID:   "trim_info_txt",
			Kind: pluginapi.UINodeText,
			Props: mustProps(pluginapi.UITextProps{
				Text:     "Click or drag on the filmstrip below, or press I / O during playback to mark IN and OUT.",
				Variant:  "muted",
				FontSize: 12,
			}),
		},
	}
	return append(nodes, renderOutputSection(s)...)
}

func renderStreamsInspector(s State) []pluginapi.UINode {
	subTab := s.StreamsSubTab
	if subTab != "subtitles" && subTab != "metadata" {
		subTab = "audio"
	}

	nodes := []pluginapi.UINode{
		{
			ID:   "streams_subtabs",
			Kind: pluginapi.UINodeTabs,
			Props: mustProps(pluginapi.UITabsProps{
				Active:  subTab,
				Compact: true,
				Tabs: []pluginapi.SurfaceOption{
					{Label: "Audio", Value: "audio"},
					{Label: "Subtitles", Value: "subtitles"},
					{Label: "Metadata", Value: "metadata"},
				},
			}),
		},
	}

	switch subTab {
	case "audio":
		audioTrackOpts := []pluginapi.SurfaceOption{
			{Label: "Keep All Audio Tracks", Value: "-1"},
		}
		if s.Probe != nil {
			for i, a := range s.Probe.AudioStreams {
				lbl := fmt.Sprintf("Track %d (%s", i+1, strings.ToUpper(a.Codec))
				if a.Language != "" {
					lbl += " · " + a.Language
				}
				lbl += ")"
				audioTrackOpts = append(audioTrackOpts, pluginapi.SurfaceOption{
					Label: lbl,
					Value: strconv.Itoa(i),
				})
			}
		}
		nodes = append(nodes, pluginapi.UINode{
			ID:   "st_mute_audio",
			Kind: pluginapi.UINodeToggle,
			Props: mustProps(pluginapi.UIToggleProps{
				Label:   "Mute / Remove Audio (-an)",
				Checked: s.Opts.MuteAudio,
			}),
		})
		if !s.Opts.MuteAudio {
			nodes = append(nodes,
				pluginapi.UINode{
					ID:   "st_audio_track",
					Kind: pluginapi.UINodeSelect,
					Props: mustProps(pluginapi.UISelectProps{
						Label:   "Audio Track",
						Value:   strconv.Itoa(s.Opts.AudioTrackIdx),
						Style:   "dropdown",
						Options: audioTrackOpts,
					}),
				},
				pluginapi.UINode{
					ID:   "st_ext_audio",
					Kind: pluginapi.UINodeFilePicker,
					Props: mustProps(pluginapi.UIFilePickerProps{
						Label:      "Replace Audio",
						Value:      s.Opts.ExternalAudioPath,
						Mode:       "open",
						Extensions: []string{"mp3", "m4a", "wav", "flac", "aac", "ogg"},
					}),
				},
				pluginapi.UINode{
					ID:   "st_loudnorm",
					Kind: pluginapi.UINodeToggle,
					Props: mustProps(pluginapi.UIToggleProps{
						Label:   "Normalize Loudness (EBU R128)",
						Checked: s.Opts.Loudnorm,
					}),
				},
			)
		}

	case "subtitles":
		nodes = append(nodes, pluginapi.UINode{
			ID:   "st_sub_mode",
			Kind: pluginapi.UINodeSelect,
			Props: mustProps(pluginapi.UISelectProps{
				Label: "Subtitle Mode",
				Value: nonEmptyStr(s.Opts.SubMode, "keep"),
				Style: "dropdown",
				Options: []pluginapi.SurfaceOption{
					{Label: "Keep Existing Subtitles", Value: "keep"},
					{Label: "Strip All Subtitles (-sn)", Value: "strip"},
					{Label: "Soft-Mux External Subtitle", Value: "soft_mux"},
					{Label: "Burn-In External Subtitle", Value: "burn"},
				},
			}),
		})
		if s.Opts.SubMode == "soft_mux" || s.Opts.SubMode == "burn" {
			nodes = append(nodes, pluginapi.UINode{
				ID:   "st_ext_sub",
				Kind: pluginapi.UINodeFilePicker,
				Props: mustProps(pluginapi.UIFilePickerProps{
					Label:      "Subtitle File",
					Value:      s.Opts.ExternalSubPath,
					Mode:       "open",
					Extensions: []string{"srt", "vtt", "ass"},
				}),
			})
		}

	case "metadata":
		nodes = append(nodes,
			pluginapi.UINode{
				ID:   "st_strip_meta",
				Kind: pluginapi.UINodeToggle,
				Props: mustProps(pluginapi.UIToggleProps{
					Label:   "Strip Container Metadata",
					Checked: s.Opts.StripMetadata,
				}),
			},
			pluginapi.UINode{
				ID:   "st_meta_title",
				Kind: pluginapi.UINodeTextInput,
				Props: mustProps(pluginapi.UITextInputProps{
					Value:       s.Opts.MetaTitle,
					Placeholder: "Title metadata (optional)",
				}),
			},
			pluginapi.UINode{
				ID:   "st_meta_artist",
				Kind: pluginapi.UINodeTextInput,
				Props: mustProps(pluginapi.UITextInputProps{
					Value:       s.Opts.MetaArtist,
					Placeholder: "Artist metadata (optional)",
				}),
			},
		)
	}

	return append(nodes, renderOutputSection(s)...)
}

func renderFramesInspector(s State) []pluginapi.UINode {
	op := nonEmptyStr(s.Opts.FramesOp, "gif")
	nodes := []pluginapi.UINode{
		{
			ID:    "sec_frames_lbl",
			Kind:  pluginapi.UINodeText,
			Props: mustProps(pluginapi.UITextProps{Text: "GIF, Frames & Contact Sheet", Variant: "heading"}),
		},
		{
			ID:   "fr_op",
			Kind: pluginapi.UINodeSelect,
			Props: mustProps(pluginapi.UISelectProps{
				Label: "Output Mode",
				Value: op,
				Style: "dropdown",
				Options: []pluginapi.SurfaceOption{
					{Label: "Animated GIF (2-Pass Palette)", Value: "gif"},
					{Label: "Animated WebP", Value: "webp"},
					{Label: "Extract Image Sequence", Value: "extract_frames"},
					{Label: "Thumbnail Contact Sheet", Value: "contact_sheet"},
				},
			}),
		},
	}

	switch op {
	case "extract_frames":
		nodes = append(nodes,
			pluginapi.UINode{
				ID:   "fr_interval",
				Kind: pluginapi.UINodeSelect,
				Props: mustProps(pluginapi.UISelectProps{
					Label: "Capture Interval",
					Value: fmt.Sprintf("%.0f", maxFloat(s.Opts.FrameIntervalSec, 2)),
					Style: "dropdown",
					Options: []pluginapi.SurfaceOption{
						{Label: "Every 1 second", Value: "1"},
						{Label: "Every 2 seconds", Value: "2"},
						{Label: "Every 5 seconds", Value: "5"},
						{Label: "Every 10 seconds", Value: "10"},
					},
				}),
			},
			pluginapi.UINode{
				ID:   "fr_format",
				Kind: pluginapi.UINodeSelect,
				Props: mustProps(pluginapi.UISelectProps{
					Label: "Image Format",
					Value: nonEmptyStr(s.Opts.FrameFormat, "png"),
					Style: "dropdown",
					Options: []pluginapi.SurfaceOption{
						{Label: "PNG (Lossless)", Value: "png"},
						{Label: "JPG (Compact)", Value: "jpg"},
					},
				}),
			},
		)
	case "contact_sheet":
		nodes = append(nodes, pluginapi.UINode{
			ID:   "fr_sheet_grid",
			Kind: pluginapi.UINodeSelect,
			Props: mustProps(pluginapi.UISelectProps{
				Label: "Grid Size",
				Value: fmt.Sprintf("%dx%d", clampIntDefault(s.Opts.SheetCols, 4), clampIntDefault(s.Opts.SheetRows, 4)),
				Style: "dropdown",
				Options: []pluginapi.SurfaceOption{
					{Label: "3 × 3 (9 frames)", Value: "3x3"},
					{Label: "4 × 4 (16 frames)", Value: "4x4"},
					{Label: "5 × 4 (20 frames)", Value: "5x4"},
					{Label: "6 × 6 (36 frames)", Value: "6x6"},
				},
			}),
		})
	default:
		nodes = append(nodes,
			pluginapi.UINode{
				ID:   "fr_gif_fps",
				Kind: pluginapi.UINodeSelect,
				Props: mustProps(pluginapi.UISelectProps{
					Label: "Frame Rate (FPS)",
					Value: strconv.Itoa(clampIntDefault(s.Opts.GIFFPS, 15)),
					Style: "dropdown",
					Options: []pluginapi.SurfaceOption{
						{Label: "10 fps (Smallest)", Value: "10"},
						{Label: "15 fps (Balanced)", Value: "15"},
						{Label: "24 fps (Smooth)", Value: "24"},
					},
				}),
			},
			pluginapi.UINode{
				ID:   "fr_gif_width",
				Kind: pluginapi.UINodeSelect,
				Props: mustProps(pluginapi.UISelectProps{
					Label: "Width",
					Value: strconv.Itoa(clampIntDefault(s.Opts.GIFWidth, 480)),
					Style: "dropdown",
					Options: []pluginapi.SurfaceOption{
						{Label: "320 px", Value: "320"},
						{Label: "480 px (Standard)", Value: "480"},
						{Label: "640 px (HD)", Value: "640"},
						{Label: "720 px", Value: "720"},
					},
				}),
			},
		)
	}
	return append(nodes, renderOutputSection(s)...)
}

func renderQueuePanel(s State) pluginapi.UINode {
	controls := pluginapi.UINode{
		ID:   "q_controls",
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Wrap: true,
			Gap:  6,
		}),
		Children: []pluginapi.UINode{
			{
				ID:   "btn_start_queue",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:    "Start",
					Variant:  "primary",
					Disabled: s.QueueRunning || len(s.QueueItems) == 0,
				}),
			},
			{
				ID:   "btn_cancel_queue",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:    "Cancel",
					Variant:  "danger",
					Disabled: !s.QueueRunning,
				}),
			},
			{
				ID:   "btn_retry_failed",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:   "Retry Failed",
					Variant: "secondary",
				}),
			},
			{
				ID:   "btn_clear_done",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:   "Clear Done",
					Variant: "ghost",
				}),
			},
		},
	}

	if len(s.QueueItems) == 0 {
		return pluginapi.UINode{
			ID:   "queue_panel",
			Kind: pluginapi.UINodeColumn,
			Props: mustProps(pluginapi.UIContainerProps{
				Gap: 10,
			}),
			Children: []pluginapi.UINode{
				controls,
				{
					ID:   "queue_empty",
					Kind: pluginapi.UINodeEmpty,
					Props: mustProps(pluginapi.UIEmptyProps{
						Title:    "Queue is empty",
						Subtitle: "Click + Queue or Export to process files.",
					}),
				},
			},
		}
	}

	rows := make([]pluginapi.UINode, 0, len(s.QueueItems))
	for _, it := range s.QueueItems {
		rows = append(rows, renderQueueItem(it))
	}

	return pluginapi.UINode{
		ID:       "queue_panel",
		Kind:     pluginapi.UINodeColumn,
		Props:    mustProps(pluginapi.UIContainerProps{Gap: 10}),
		Children: append([]pluginapi.UINode{controls}, rows...),
	}
}

func renderQueueItem(it queue.Item) pluginapi.UINode {
	topRow := pluginapi.UINode{
		ID:   "qi_top_" + it.ID,
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Align:   "center",
			Justify: "between",
			Gap:     6,
		}),
		Children: []pluginapi.UINode{
			{
				ID:   "qi_title_" + it.ID,
				Kind: pluginapi.UINodeText,
				Props: mustProps(pluginapi.UITextProps{
					Text:       it.Title,
					Variant:    "body",
					SingleLine: true,
					Grow:       true,
				}),
			},
			{
				ID:   "qi_status_" + it.ID,
				Kind: pluginapi.UINodeText,
				Props: mustProps(pluginapi.UITextProps{
					Text:    strings.ToUpper(string(it.Status)),
					Variant: statusVariant(it.Status),
				}),
			},
		},
	}

	kids := []pluginapi.UINode{topRow}
	if it.Label != "" {
		kids = append(kids, pluginapi.UINode{
			ID:   "qi_lbl_" + it.ID,
			Kind: pluginapi.UINodeText,
			Props: mustProps(pluginapi.UITextProps{
				Text:       it.Label,
				Variant:    "muted",
				SingleLine: true,
			}),
		})
	}
	if it.Status == queue.StatusRunning || it.Progress > 0 {
		kids = append(kids, pluginapi.UINode{
			ID:   "qi_prog_" + it.ID,
			Kind: pluginapi.UINodeProgress,
			Props: mustProps(pluginapi.UIProgressProps{
				Done:  it.Progress,
				Total: 100,
				Label: it.Message,
			}),
		})
	}
	if it.Error != "" {
		kids = append(kids, pluginapi.UINode{
			ID:   "qi_err_" + it.ID,
			Kind: pluginapi.UINodeText,
			Props: mustProps(pluginapi.UITextProps{
				Text:    it.Error,
				Variant: "danger",
			}),
		})
	}

	var actBtns []pluginapi.UINode
	if len(it.Outputs) > 0 && it.Status == queue.StatusDone {
		actBtns = append(actBtns, pluginapi.UINode{
			ID:   "q_reveal_" + it.ID,
			Kind: pluginapi.UINodeButton,
			Props: mustProps(pluginapi.UIButtonProps{
				Label:       "Reveal",
				Variant:     "secondary",
				ShellAction: "reveal",
				ShellPath:   it.Outputs[0],
			}),
		})
	}
	if it.Status != queue.StatusRunning {
		actBtns = append(actBtns, pluginapi.UINode{
			ID:   "q_remove_" + it.ID,
			Kind: pluginapi.UINodeButton,
			Props: mustProps(pluginapi.UIButtonProps{
				Label:   "Remove",
				Variant: "ghost",
			}),
		})
	}
	if len(actBtns) > 0 {
		kids = append(kids, pluginapi.UINode{
			ID:       "qi_acts_" + it.ID,
			Kind:     pluginapi.UINodeRow,
			Props:    mustProps(pluginapi.UIContainerProps{Gap: 6, Wrap: true}),
			Children: actBtns,
		})
	}

	return pluginapi.UINode{
		ID:   "qi_" + it.ID,
		Kind: pluginapi.UINodeColumn,
		Props: mustProps(pluginapi.UIContainerProps{
			Surface: "ground",
			Border:  true,
			Padding: 8,
			Gap:     4,
		}),
		Children: kids,
	}
}

func renderTimelineDeck(s State) pluginapi.UINode {
	dur := 0.0
	hasVideo := false
	var chapters []engine.ChapterInfo
	if s.Probe != nil {
		dur = s.Probe.DurationSec
		hasVideo = s.Probe.HasVideo()
		chapters = s.Probe.Chapters
	}
	disabled := s.Probe == nil || dur <= 0
	trackW := ComputeTrackWidth(s.ViewportW)

	clipDur := maxFloat(0, s.EndSec-s.StartSec)
	clipBadgeText := "CLIP " + FormatTimestamp(clipDur)
	if s.Probe != nil {
		estBytes, _, ok := engine.EstimateOutputBytes(s.Probe, s.Mode, s.Opts, clipDur)
		if ok && estBytes > 0 {
			clipBadgeText = fmt.Sprintf("CLIP %s · Est. %s", FormatTimestamp(clipDur), formatBytesHuman(estBytes))
		}
	}

	playLabel, playIcon := playButtonState(s)
	volIcon := "volume"
	if s.Muted || s.Volume <= 0.01 {
		volIcon = "volume-mute"
	}
	rate := s.PlaybackRate
	if rate <= 0 {
		rate = 1.0
	}

	toolsKids := []pluginapi.UINode{
		{
			ID:   "btn_speed_cycle",
			Kind: pluginapi.UINodeButton,
			Props: mustProps(pluginapi.UIButtonProps{
				Label:    fmt.Sprintf("%.2g×", rate),
				Variant:  "ghost",
				Height:   28,
				PaddingX: 6,
				PaddingY: 3,
				Disabled: disabled,
				Tooltip:  "Cycle playback speed",
			}),
		},
		{
			ID:   "btn_mute_toggle",
			Kind: pluginapi.UINodeButton,
			Props: mustProps(pluginapi.UIButtonProps{
				Icon:     volIcon,
				IconOnly: true,
				Variant:  "ghost",
				Width:    28,
				Height:   28,
				Disabled: disabled,
				Tooltip:  "Mute / Unmute (M)",
			}),
		},
		{
			ID:   "vol_slider",
			Kind: pluginapi.UINodeSlider,
			Props: mustProps(pluginapi.UISliderProps{
				Min:      0,
				Max:      1,
				Step:     0.05,
				Value:    s.Volume,
				Width:    84,
				Disabled: disabled,
			}),
		},
		{
			ID:   "btn_loop_toggle",
			Kind: pluginapi.UINodeButton,
			Props: mustProps(pluginapi.UIButtonProps{
				Label:    "Loop",
				Icon:     "repeat",
				Variant:  "secondary",
				Selected: s.LoopSelection,
				Height:   28,
				PaddingX: 8,
				PaddingY: 3,
				Disabled: disabled,
				Tooltip:  "Loop between IN and OUT (Shift+L)",
			}),
		},
	}
	if hasVideo {
		fitLabel := "Fill"
		if s.EffectiveVideoFit() == "cover" {
			fitLabel = "Fit"
		}
		toolsKids = append(toolsKids,
			pluginapi.UINode{
				ID:   "btn_video_fit",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:    fitLabel,
					Variant:  "secondary",
					Selected: s.EffectiveVideoFit() == "cover",
					Height:   28,
					PaddingX: 8,
					PaddingY: 3,
					Tooltip:  "Toggle Video Fill / Fit on canvas",
				}),
			},
			pluginapi.UINode{
				ID:   "btn_snapshot_hd",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Label:    "HD",
					Icon:     "camera",
					Variant:  "secondary",
					Height:   28,
					PaddingX: 8,
					PaddingY: 3,
					Disabled: !s.HasFFmpeg || disabled,
					Tooltip:  "Save HD PNG snapshot at playhead (S)",
				}),
			},
			pluginapi.UINode{
				ID:   "btn_fullscreen",
				Kind: pluginapi.UINodeButton,
				Props: mustProps(pluginapi.UIButtonProps{
					Icon:     "maximize",
					IconOnly: true,
					Variant:  "secondary",
					Width:    28,
					Height:   28,
					Tooltip:  "Fullscreen Monitor (F)",
				}),
			},
		)
	}

	transportRow := pluginapi.UINode{
		ID:   "tl_transport_row",
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Wrap:    true,
			Gap:     8,
			Align:   "center",
			Justify: "between",
		}),
		Children: []pluginapi.UINode{
			{
				ID:   "tl_time_grp",
				Kind: pluginapi.UINodeRow,
				Props: mustProps(pluginapi.UIContainerProps{
					Gap:         8,
					Align:       "center",
					NoFillWidth: true,
				}),
				Children: []pluginapi.UINode{
					{
						ID:   "mon_timecode",
						Kind: pluginapi.UINodeText,
						Props: mustProps(pluginapi.UITextProps{
							Text:          fmt.Sprintf("%s / %s", FormatTimecodePrecise(s.PlayheadSec), FormatTimecodePrecise(dur)),
							FontSize:      13,
							FontWeight:    700,
							TabularNums:   true,
							SingleLine:    true,
							BindVideoTime: "video_player",
							BindDuration:  dur,
						}),
					},
					{
						ID:   "tl_clip_badge",
						Kind: pluginapi.UINodeText,
						Props: mustProps(pluginapi.UITextProps{
							Text:        clipBadgeText,
							Variant:     "muted",
							FontSize:    12,
							TabularNums: true,
							SingleLine:  true,
						}),
					},
				},
			},
			{
				ID:   "btn_transport_row",
				Kind: pluginapi.UINodeRow,
				Props: mustProps(pluginapi.UIContainerProps{
					Gap:         4,
					Align:       "center",
					NoFillWidth: true,
				}),
				Children: []pluginapi.UINode{
					{
						ID:   "btn_jump_in",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Icon: "skip-back", IconOnly: true, Variant: "secondary", Width: 28, Height: 28, Disabled: disabled,
							Tooltip: "Jump to IN / Start (Home)",
						}),
					},
					{
						ID:   "btn_skip_back",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Label: "-5s", Variant: "secondary", Height: 28, PaddingX: 7, PaddingY: 3, Disabled: disabled,
							Tooltip: "Back 5 seconds (J)",
						}),
					},
					{
						ID:   "btn_step_back",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Icon: "step-back", IconOnly: true, Variant: "secondary", Width: 28, Height: 28, Disabled: disabled,
							Tooltip: "Previous Frame (,)",
						}),
					},
					{
						ID:   "btn_play_pause",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Label: playLabel, Icon: playIcon, Variant: "primary", Height: 30, PaddingX: 12, PaddingY: 4, Disabled: disabled,
							Tooltip: "Play / Pause / Resume (Space or K)",
						}),
					},
					{
						ID:   "btn_step_fwd",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Icon: "step-forward", IconOnly: true, Variant: "secondary", Width: 28, Height: 28, Disabled: disabled,
							Tooltip: "Next Frame (.)",
						}),
					},
					{
						ID:   "btn_skip_fwd",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Label: "+5s", Variant: "secondary", Height: 28, PaddingX: 7, PaddingY: 3, Disabled: disabled,
							Tooltip: "Forward 5 seconds (L)",
						}),
					},
					{
						ID:   "btn_jump_out",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Icon: "skip-forward", IconOnly: true, Variant: "secondary", Width: 28, Height: 28, Disabled: disabled,
							Tooltip: "Jump to OUT / End (End)",
						}),
					},
				},
			},
			{
				ID:   "tl_tools_grp",
				Kind: pluginapi.UINodeRow,
				Props: mustProps(pluginapi.UIContainerProps{
					Gap:         6,
					Align:       "center",
					NoFillWidth: true,
					Wrap:        true,
				}),
				Children: toolsKids,
			},
		},
	}

	midKids := []pluginapi.UINode{
		{
			ID:   "btn_add_clip",
			Kind: pluginapi.UINodeButton,
			Props: mustProps(pluginapi.UIButtonProps{
				Label:    "+ Clip",
				Variant:  "secondary",
				Height:   26,
				PaddingX: 8,
				PaddingY: 3,
				Disabled: disabled,
			}),
		},
	}
	if len(chapters) > 0 {
		midKids = append(midKids, pluginapi.UINode{
			ID:   "btn_split_chapters",
			Kind: pluginapi.UINodeButton,
			Props: mustProps(pluginapi.UIButtonProps{
				Label:    fmt.Sprintf("Split %d Chapters", len(chapters)),
				Variant:  "secondary",
				Height:   26,
				PaddingX: 8,
				PaddingY: 3,
				Disabled: disabled,
			}),
		})
	}
	midKids = append(midKids, pluginapi.UINode{
		ID:   "btn_trim_reset",
		Kind: pluginapi.UINodeButton,
		Props: mustProps(pluginapi.UIButtonProps{
			Label:    "Reset",
			Variant:  "ghost",
			Height:   26,
			PaddingX: 8,
			PaddingY: 3,
			Disabled: disabled,
		}),
	})

	controlsRow := pluginapi.UINode{
		ID:   "tl_controls",
		Kind: pluginapi.UINodeRow,
		Props: mustProps(pluginapi.UIContainerProps{
			Wrap:    true,
			Gap:     8,
			Align:   "center",
			Justify: "between",
		}),
		Children: []pluginapi.UINode{
			{
				ID:   "tl_in_grp",
				Kind: pluginapi.UINodeRow,
				Props: mustProps(pluginapi.UIContainerProps{
					Gap:         4,
					Align:       "center",
					NoFillWidth: true,
				}),
				Children: []pluginapi.UINode{
					{
						ID:    "tl_in_lbl",
						Kind:  pluginapi.UINodeText,
						Props: mustProps(pluginapi.UITextProps{Text: "IN", Variant: "muted", FontWeight: 700}),
					},
					{
						ID:   "btn_mark_in",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Label: "[ IN", Variant: "secondary", Height: 26, PaddingX: 7, PaddingY: 3, Disabled: disabled,
							Tooltip: "Set IN point at playhead (I)",
						}),
					},
					{
						ID:   "time_start_input",
						Kind: pluginapi.UINodeTextInput,
						Props: mustProps(pluginapi.UITextInputProps{
							Value:       s.StartText,
							Placeholder: "00:00",
							Width:       68,
							Disabled:    disabled,
						}),
					},
					{
						ID:   "btn_start_minus",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Label: "-1s", Variant: "ghost", Height: 26, PaddingX: 6, PaddingY: 3, Disabled: disabled,
						}),
					},
					{
						ID:   "btn_start_plus",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Label: "+1s", Variant: "ghost", Height: 26, PaddingX: 6, PaddingY: 3, Disabled: disabled,
						}),
					},
				},
			},
			{
				ID:   "tl_mid_grp",
				Kind: pluginapi.UINodeRow,
				Props: mustProps(pluginapi.UIContainerProps{
					Gap:         6,
					Align:       "center",
					NoFillWidth: true,
					Wrap:        true,
				}),
				Children: midKids,
			},
			{
				ID:   "tl_out_grp",
				Kind: pluginapi.UINodeRow,
				Props: mustProps(pluginapi.UIContainerProps{
					Gap:         4,
					Align:       "center",
					NoFillWidth: true,
				}),
				Children: []pluginapi.UINode{
					{
						ID:   "btn_end_minus",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Label: "-1s", Variant: "ghost", Height: 26, PaddingX: 6, PaddingY: 3, Disabled: disabled,
						}),
					},
					{
						ID:   "btn_end_plus",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Label: "+1s", Variant: "ghost", Height: 26, PaddingX: 6, PaddingY: 3, Disabled: disabled,
						}),
					},
					{
						ID:   "time_end_input",
						Kind: pluginapi.UINodeTextInput,
						Props: mustProps(pluginapi.UITextInputProps{
							Value:       s.EndText,
							Placeholder: "00:00",
							Width:       68,
							Disabled:    disabled,
						}),
					},
					{
						ID:   "btn_mark_out",
						Kind: pluginapi.UINodeButton,
						Props: mustProps(pluginapi.UIButtonProps{
							Label: "OUT ]", Variant: "secondary", Height: 26, PaddingX: 7, PaddingY: 3, Disabled: disabled,
							Tooltip: "Set OUT point at playhead (O)",
						}),
					},
					{
						ID:    "tl_out_lbl",
						Kind:  pluginapi.UINodeText,
						Props: mustProps(pluginapi.UITextProps{Text: "OUT", Variant: "muted", FontWeight: 700}),
					},
				},
			},
		},
	}

	stripFrame := BuildTimelineNode(trackW, dur, s.StartSec, s.EndSec, s.PlayheadSec, s.StripAsset, chapters, s.Clips, disabled)
	deckKids := []pluginapi.UINode{transportRow, stripFrame, controlsRow}

	if len(s.Clips) > 0 {
		clipChips := make([]pluginapi.UINode, 0, len(s.Clips)*2)
		for _, cl := range s.Clips {
			clipChips = append(clipChips,
				pluginapi.UINode{
					ID:   "clip_select_" + cl.ID,
					Kind: pluginapi.UINodeButton,
					Props: mustProps(pluginapi.UIButtonProps{
						Label:   fmt.Sprintf("%s (%s–%s)", cl.Title, FormatTimestamp(cl.StartSec), FormatTimestamp(cl.EndSec)),
						Variant: "secondary",
					}),
				},
				pluginapi.UINode{
					ID:   "clip_remove_" + cl.ID,
					Kind: pluginapi.UINodeButton,
					Props: mustProps(pluginapi.UIButtonProps{
						Label:   "×",
						Variant: "ghost",
					}),
				},
			)
		}
		deckKids = append(deckKids, pluginapi.UINode{
			ID:       "clips_chip_row",
			Kind:     pluginapi.UINodeRow,
			Props:    mustProps(pluginapi.UIContainerProps{Wrap: true, Gap: 4}),
			Children: clipChips,
		})
	}

	return pluginapi.UINode{
		ID:   "timeline_deck",
		Kind: pluginapi.UINodeColumn,
		Props: mustProps(pluginapi.UIContainerProps{
			Surface:     "paper",
			BorderSides: [4]int{2, 0, 0, 0},
			BorderToken: "ink",
			PaddingH:    20,
			PaddingV:    14,
			Gap:         10,
			Align:       "center",
		}),
		Children: deckKids,
	}
}

func crfQualityHint(crf int) string {
	switch {
	case crf <= 18:
		return "Visually lossless — largest output file"
	case crf <= 23:
		return "Balanced quality — recommended for most videos"
	case crf <= 28:
		return "Compact file size — good for web sharing"
	default:
		return "Aggressive compression — smallest file size"
	}
}

func statusVariant(st queue.Status) string {
	switch st {
	case queue.StatusDone:
		return "success"
	case queue.StatusFailed:
		return "danger"
	case queue.StatusRunning:
		return "accent"
	default:
		return "muted"
	}
}

func formatBytesHuman(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const mb = 1024 * 1024
	const kb = 1024
	if b >= mb {
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	}
	if b >= kb {
		return fmt.Sprintf("%.0f KB", float64(b)/float64(kb))
	}
	return fmt.Sprintf("%d B", b)
}

func nonEmptyStr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func clampIntDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
