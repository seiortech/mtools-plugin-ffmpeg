package queue

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/seiortech/mtools-plugin-ffmpeg/internal/engine"
	"github.com/seiortech/mtools-sdk/pluginapi"
)

type Status string

const (
	StatusQueued   Status = "queued"
	StatusRunning  Status = "running"
	StatusDone     Status = "done"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

type Item struct {
	ID       string
	Title    string
	Label    string
	Spec     engine.JobSpec
	Plan     engine.CommandPlan
	Status   Status
	Progress float64
	Message  string
	Outputs  []string
	Error    string
}

type Executor func(ctx context.Context, item Item, onProgress func(pluginapi.Progress)) (pluginapi.RunResult, error)

// Manager coordinates a bounded parallel transcode queue (1..2 workers).
type Manager struct {
	mu       sync.Mutex
	items    []Item
	nextID   int
	running  bool
	cancelFn context.CancelFunc
	onChange func()
}

func NewManager(onChange func()) *Manager {
	return &Manager{onChange: onChange}
}

func (m *Manager) Add(title, label string, spec engine.JobSpec, plan engine.CommandPlan) string {
	m.mu.Lock()
	m.nextID++
	id := fmt.Sprintf("ff%d", m.nextID)
	if strings.TrimSpace(title) == "" && len(spec.InputPaths) > 0 {
		title = filepath.Base(spec.InputPaths[0])
	}
	spec.ID = id
	m.items = append(m.items, Item{
		ID:      id,
		Title:   title,
		Label:   label,
		Spec:    spec,
		Plan:    plan,
		Status:  StatusQueued,
		Message: "Queued",
	})
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb()
	}
	return id
}

func (m *Manager) Remove(id string) {
	m.mu.Lock()
	filtered := m.items[:0]
	for _, it := range m.items {
		if it.ID == id && it.Status != StatusRunning {
			continue
		}
		filtered = append(filtered, it)
	}
	m.items = filtered
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (m *Manager) ClearCompleted() {
	m.mu.Lock()
	filtered := m.items[:0]
	for _, it := range m.items {
		if it.Status == StatusDone || it.Status == StatusCanceled {
			continue
		}
		filtered = append(filtered, it)
	}
	m.items = filtered
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (m *Manager) RetryFailed() int {
	m.mu.Lock()
	count := 0
	for i := range m.items {
		if m.items[i].Status == StatusFailed || m.items[i].Status == StatusCanceled {
			m.items[i].Status = StatusQueued
			m.items[i].Progress = 0
			m.items[i].Error = ""
			m.items[i].Message = "Queued for retry"
			count++
		}
	}
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil && count > 0 {
		cb()
	}
	return count
}

func (m *Manager) CancelActive() {
	m.mu.Lock()
	cancel := m.cancelFn
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) Snapshot() ([]Item, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Item, len(m.items))
	copy(out, m.items)
	return out, m.running
}

func (m *Manager) Start(parentCtx context.Context, workers int, execFn Executor) {
	if workers < 1 {
		workers = 1
	}
	if workers > 2 {
		workers = 2
	}

	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parentCtx)
	m.running = true
	m.cancelFn = cancel
	m.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			m.mu.Lock()
			m.running = false
			m.cancelFn = nil
			cb := m.onChange
			m.mu.Unlock()
			if cb != nil {
				cb()
			}
		}()

		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					item, idx, ok := m.claimNext()
					if !ok {
						return
					}
					if ctx.Err() != nil {
						m.finishItem(idx, StatusCanceled, 0, "Canceled", nil, "")
						return
					}

					res, err := execFn(ctx, item, func(p pluginapi.Progress) {
						m.updateProgress(idx, p.Done, p.Message)
					})
					if err != nil {
						if errors.Is(err, context.Canceled) || ctx.Err() != nil {
							m.finishItem(idx, StatusCanceled, 0, "Canceled", nil, "")
						} else {
							m.finishItem(idx, StatusFailed, 0, "Failed", nil, err.Error())
						}
						continue
					}
					msg := res.Summary
					if msg == "" && len(res.Outputs) > 0 {
						msg = "Saved " + filepath.Base(res.Outputs[0])
					}
					m.finishItem(idx, StatusDone, 100, msg, res.Outputs, "")
				}
			}()
		}
		wg.Wait()
	}()
}

func (m *Manager) claimNext() (Item, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.items {
		if m.items[i].Status == StatusQueued {
			m.items[i].Status = StatusRunning
			m.items[i].Progress = 0
			m.items[i].Error = ""
			m.items[i].Message = "Starting..."
			it := m.items[i]
			cb := m.onChange
			if cb != nil {
				go cb()
			}
			return it, i, true
		}
	}
	return Item{}, -1, false
}

func (m *Manager) updateProgress(idx int, pct float64, msg string) {
	m.mu.Lock()
	if idx >= 0 && idx < len(m.items) && m.items[idx].Status == StatusRunning {
		m.items[idx].Progress = pct
		if msg != "" {
			m.items[idx].Message = msg
		}
	}
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (m *Manager) finishItem(idx int, st Status, pct float64, msg string, outputs []string, errMsg string) {
	m.mu.Lock()
	if idx >= 0 && idx < len(m.items) {
		m.items[idx].Status = st
		m.items[idx].Progress = pct
		m.items[idx].Message = msg
		m.items[idx].Outputs = outputs
		m.items[idx].Error = errMsg
	}
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb()
	}
}
