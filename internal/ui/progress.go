package ui

import (
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/BayInl/session-finder/internal/record"
	"github.com/jedib0t/go-pretty/v6/progress"
	"github.com/jedib0t/go-pretty/v6/text"
)

// IndexProgress draws a TTY spinner/bar on w. Non-TTY only prints source warnings.
type IndexProgress struct {
	w       io.Writer
	tty     bool
	mu      sync.Mutex
	calls   int
	pw      progress.Writer
	spinner *progress.Tracker
	bar     *progress.Tracker
	done    chan struct{}
	closed  bool
}

func NewIndexProgress(w io.Writer) *IndexProgress {
	if w == nil {
		w = io.Discard
	}
	return &IndexProgress{w: w, tty: IsTTY(w)}
}

func (p *IndexProgress) Report(done, total int, spec record.SourceSpec, err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		if err != nil && !p.tty {
			fmt.Fprintf(p.w, "warning: failed to index %s: %v\n", spec.Path, err)
		}
		return
	}
	p.calls++
	if !p.tty {
		if err != nil {
			fmt.Fprintf(p.w, "warning: failed to index %s: %v\n", spec.Path, err)
		}
		return
	}
	switch p.calls {
	case 1:
		p.startWriter()
		p.spinner = &progress.Tracker{Message: "discovering sources", Total: 0, RemoveOnCompletion: true}
		p.pw.AppendTracker(p.spinner)
	case 2:
		if p.spinner != nil && !p.spinner.IsDone() {
			p.spinner.MarkAsDone()
		}
		if total > 0 {
			p.bar = &progress.Tracker{Message: "indexing", Total: int64(total)}
			p.pw.AppendTracker(p.bar)
		}
	default:
		if p.bar != nil {
			msg := spec.Tool
			if spec.Path != "" {
				if msg == "" {
					msg = filepath.Base(spec.Path)
				} else {
					msg = spec.Tool + " " + filepath.Base(spec.Path)
				}
			}
			if msg != "" {
				p.bar.UpdateMessage(msg)
			}
			p.bar.Increment(1)
		}
		if err != nil {
			p.logWarningLocked(spec, err)
		}
		_ = done
	}
}

func (p *IndexProgress) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	if p.spinner != nil && !p.spinner.IsDone() {
		p.spinner.MarkAsDone()
	}
	if p.bar != nil && !p.bar.IsDone() {
		p.bar.MarkAsDone()
	}
	if p.pw != nil {
		p.pw.Stop()
	}
	done := p.done
	p.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (p *IndexProgress) startWriter() {
	pw := progress.NewWriter()
	pw.SetOutputWriter(p.w)
	pw.SetAutoStop(false)
	pw.SetTerminalWidth(Width(p.w))
	pw.SetTrackerLength(20)
	pw.ShowETA(false)
	pw.ShowOverallTracker(false)
	pw.SetUpdateFrequency(100 * time.Millisecond)
	style := progress.StyleDefault
	style.Colors = progress.StyleColors{}
	if ColorEnabled(p.w) {
		style.Colors = progress.StyleColors{
			Message: text.Colors{text.FgBlue},
			Error:   text.Colors{text.FgRed},
			Percent: text.Colors{text.FgGreen},
			Stats:   text.Colors{text.Faint},
			Time:    text.Colors{text.Faint},
			Tracker: text.Colors{text.Faint},
			Value:   text.Colors{text.FgGreen},
			Speed:   text.Colors{text.Faint},
		}
	}
	pw.SetStyle(style)
	p.pw = pw
	p.done = make(chan struct{})
	go func() {
		pw.Render()
		close(p.done)
	}()
	deadline := time.Now().Add(time.Second)
	for !pw.IsRenderInProgress() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

func (p *IndexProgress) logWarningLocked(spec record.SourceSpec, err error) {
	prefix := "warning:"
	if ColorEnabled(p.w) {
		prefix = NewTheme(p.w).Style(TokenWarn).Render("warning:")
	}
	msg := fmt.Sprintf("%s failed to index %s: %v", prefix, spec.Path, err)
	if p.pw != nil && p.pw.IsRenderInProgress() {
		p.pw.Log("%s", msg)
		return
	}
	fmt.Fprintln(p.w, msg)
}
