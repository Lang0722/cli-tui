package tui

import (
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

// printer feeds committed transcript lines into the terminal scrollback
// (Bubble Tea's insertAbove) from its own goroutine, strictly in order.
//
// Constraints that shape it:
//   - Update must never block on it: Println needs the event loop that runs
//     Update, so a bounded channel between them can deadlock (e.g. replaying
//     a long session inside one Update). The queue is therefore unbounded.
//   - insertAbove scrolls the screen by the inserted height, so a multi-line
//     insert taller than the free rows above a big live frame pushes frame
//     rows into scrollback. One line per insert is always safe while the
//     frame is shorter than the terminal (View clamps it).
//   - The frame buffer changes on every Update but reaches the terminal on
//     the next frame tick; an insert in between is positioned with the new
//     frame height over the old frame and leaves part of it behind. Waiting
//     a couple of frames before a batch lets that flush happen first.
//   - Inserts while the alternate screen (Ctrl+T pager) is shown would be
//     written into the alt buffer and lost, so printing can be held.
type printer struct {
	mu      sync.Mutex
	cond    *sync.Cond
	queue   []string // individual lines
	held    holdReason
	busy    bool // a line is being handed to the program
	stopped bool
	wake    chan struct{}
}

// holdReason says why output is paused; output flows when none is set.
type holdReason uint8

const (
	holdPager holdReason = 1 << iota // alt-screen pager is open
	holdClear                        // the terminal is being cleared for a reflow
)

func newPrinter() *printer {
	p := &printer{wake: make(chan struct{}, 1)}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// push queues text (may contain newlines). Never blocks.
func (p *printer) push(s string) {
	p.mu.Lock()
	p.queue = append(p.queue, strings.Split(s, "\n")...)
	p.mu.Unlock()
	p.signal()
}

func (p *printer) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// hold pauses (on) or resumes output for one reason.
func (p *printer) hold(r holdReason, on bool) {
	p.mu.Lock()
	if on {
		p.held |= r
	} else {
		p.held &^= r
	}
	p.mu.Unlock()
	p.signal()
	p.cond.Broadcast()
}

// releaseAll resumes output regardless of reason (quitting).
func (p *printer) releaseAll() {
	p.mu.Lock()
	p.held = 0
	p.mu.Unlock()
	p.signal()
	p.cond.Broadcast()
}

// run prints until stop is called. println is tea.Program.Println. Lines
// are taken one at a time so a hold (pager opening, terminal clearing) or a
// reset takes effect between lines, never mid-batch.
func (p *printer) run(println func(...any)) {
	for {
		<-p.wake
		time.Sleep(35 * time.Millisecond)
		for {
			p.mu.Lock()
			if p.stopped {
				p.mu.Unlock()
				return
			}
			if p.held != 0 || len(p.queue) == 0 {
				p.mu.Unlock()
				p.cond.Broadcast()
				break
			}
			line := p.queue[0]
			p.queue = p.queue[1:]
			p.busy = true
			p.mu.Unlock()

			if line == "" {
				line = " " // Bubble Tea drops empty prints
			}
			println(line)

			p.mu.Lock()
			p.busy = false
			p.mu.Unlock()
			p.cond.Broadcast()
		}
	}
}

// drained blocks until everything queued has been handed to the program,
// or the timeout passes. Must not be called from Update.
func (p *printer) drained(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	go func() { // wake cond waiters at the deadline
		time.Sleep(timeout)
		p.cond.Broadcast()
	}()
	p.mu.Lock()
	defer p.mu.Unlock()
	for (len(p.queue) > 0 || p.busy) && p.held == 0 && time.Now().Before(deadline) {
		p.signal()
		p.cond.Wait()
	}
}

func (p *printer) stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	p.signal()
}

// quitCmd waits for pending scrollback output, then quits. Bubble Tea
// discards prints still queued when it receives QuitMsg.
func (p *printer) quitCmd() tea.Cmd {
	return func() tea.Msg {
		p.drained(2 * time.Second)
		return tea.QuitMsg{}
	}
}

// reset drops everything queued (the transcript is about to be re-emitted).
func (p *printer) reset() {
	p.mu.Lock()
	p.queue = nil
	p.mu.Unlock()
}

// takeQueued removes and returns everything queued (for tests, which run
// without a Bubble Tea program to print into).
func (p *printer) takeQueued() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := p.queue
	p.queue = nil
	return q
}
