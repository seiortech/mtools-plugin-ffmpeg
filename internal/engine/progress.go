package engine

import (
	"fmt"
	"strconv"
	"strings"
)

type ProgressState struct {
	OutTimeSec float64
	Speed      string
	TotalBytes int64
}

// ParseProgressKeyVal parses `ffmpeg -progress pipe:1` key=value lines without regex allocations.
func ParseProgressKeyVal(line string, st *ProgressState, targetDurSec float64) (pct float64, detail string, done bool, updated bool) {
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return 0, "", false, false
	}
	key := strings.TrimSpace(line[:eq])
	val := strings.TrimSpace(line[eq+1:])

	switch key {
	case "out_time_us":
		if us, err := strconv.ParseInt(val, 10, 64); err == nil && us >= 0 {
			st.OutTimeSec = float64(us) / 1_000_000.0
		}
	case "out_time_ms":
		// ffmpeg writes microseconds in out_time_ms in some versions; prefer out_time_us when present.
		if st.OutTimeSec == 0 {
			if ms, err := strconv.ParseInt(val, 10, 64); err == nil && ms >= 0 {
				st.OutTimeSec = float64(ms) / 1_000_000.0
			}
		}
	case "speed":
		if val != "N/A" {
			st.Speed = val
		}
	case "total_size":
		if sz, err := strconv.ParseInt(val, 10, 64); err == nil && sz >= 0 {
			st.TotalBytes = sz
		}
	case "progress":
		updated = true
		if val == "end" {
			done = true
			pct = 100
		} else if targetDurSec > 0 {
			pct = (st.OutTimeSec / targetDurSec) * 100
			if pct < 0 {
				pct = 0
			}
			if pct > 99.5 {
				pct = 99.5
			}
		}
		var parts []string
		if targetDurSec > 0 {
			parts = append(parts, fmt.Sprintf("%.0f%%", pct))
		}
		if st.Speed != "" {
			parts = append(parts, st.Speed)
		}
		if st.TotalBytes > 0 {
			parts = append(parts, fmt.Sprintf("%.1f MB", float64(st.TotalBytes)/(1024*1024)))
		}
		detail = strings.Join(parts, " · ")
	}
	return pct, detail, done, updated
}
