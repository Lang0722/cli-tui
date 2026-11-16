// Package e2e drives the real agenttui binary in a real terminal emulator
// (tmux), typing keys the way a user does and reading the screen back.
//
//	go test ./e2e            # needs tmux; skipped with -short or without it
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var (
	binary string // built once by TestMain
	seq    atomic.Int64
)

func TestMain(m *testing.M) {
	code := func() int {
		if _, err := exec.LookPath("tmux"); err != nil {
			fmt.Println("e2e: tmux not found, skipping")
			return 0
		}
		dir, err := os.MkdirTemp("", "agenttui-e2e")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(dir)
		binary = filepath.Join(dir, "agenttui")
		build := exec.Command("go", "build", "-o", binary, "../cmd/agenttui")
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err := build.Run(); err != nil {
			fmt.Println("e2e: build failed:", err)
			return 1
		}
		return m.Run()
	}()
	os.Exit(code)
}

// term is one agenttui process in its own tmux server.
type term struct {
	t      *testing.T
	socket string
	home   string // HOME / XDG dirs / sessions live here
	cwd    string
}

const (
	termW = 110
	termH = 40
)

type opts struct {
	args  []string // agenttui arguments
	cwd   string   // working directory (default: a temp dir with a few files)
	home  string   // reuse another run's HOME (sessions, config, history)
	reset bool     // -reset-demo: install the demo history
	ready string   // screen text that means started (default "? for shortcuts")
}

func start(t *testing.T, o opts) *term {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e: -short")
	}
	s := &term{t: t, socket: fmt.Sprintf("agenttui-e2e-%d-%d", os.Getpid(), seq.Add(1)), home: o.home, cwd: o.cwd}
	if s.home == "" {
		s.home = t.TempDir()
	}
	if s.cwd == "" {
		s.cwd = t.TempDir()
		writeFile(t, filepath.Join(s.cwd, "main.go"), "package main\n\nfunc main() {}\n")
		writeFile(t, filepath.Join(s.cwd, "README.md"), "# demo\n")
		writeFile(t, filepath.Join(s.cwd, "internal", "server.go"), "package internal\n")
	}
	args := []string{"-config", filepath.Join(s.home, "config.toml"), "-notify=false"}
	if !contains(o.args, "--") {
		args = append(args, "-sessions", filepath.Join(s.home, "sessions"))
	}
	if o.reset {
		args = append(args, "-reset-demo")
	}
	// "resume" must come first on the command line.
	if len(o.args) > 0 && o.args[0] == "resume" {
		args = append([]string{"resume"}, append(args, o.args[1:]...)...)
	} else {
		args = append(args, o.args...)
	}
	env := []string{
		"HOME=" + s.home, "XDG_CONFIG_HOME=" + filepath.Join(s.home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(s.home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(s.home, ".local", "state"),
		"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "TERM_PROGRAM=", "VISUAL=", "EDITOR=",
	}
	cmd := "env -i " + shellJoin(env) + " " + shellJoin(append([]string{binary}, args...))
	s.tmux("-f", "/dev/null", "new-session", "-d", "-x", fmt.Sprint(termW), "-y", fmt.Sprint(termH), "-c", s.cwd, cmd)
	s.tmux("set-option", "-g", "remain-on-exit", "on")
	s.tmux("set-option", "-g", "history-limit", "50000")
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("final screen:\n%s", s.screen())
		}
		_ = exec.Command("tmux", "-L", s.socket, "kill-server").Run()
	})
	if o.ready == "" {
		// the footer, then the header box drawn for session_configured
		s.waitFor("? for shortcuts")
		o.ready = ">_ "
	}
	s.waitFor(o.ready)
	return s
}

func (s *term) tmux(args ...string) string {
	s.t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", s.socket}, args...)...).CombinedOutput()
	if err != nil {
		s.t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// keys sends tmux key names: "Enter", "Escape", "C-c", "Up", "M-Left", ...
func (s *term) keys(k ...string) {
	s.t.Helper()
	for _, key := range k {
		s.tmux("send-keys", "-t", "0", key)
		if key == "Escape" {
			// A real Esc press is never followed within a few ms by the next
			// key; if it is, terminals and Bubble Tea read the pair as Alt+key.
			time.Sleep(250 * time.Millisecond)
		} else {
			time.Sleep(40 * time.Millisecond)
		}
	}
}

// typ types text literally.
func (s *term) typ(text string) {
	s.t.Helper()
	s.tmux("send-keys", "-t", "0", "-l", text)
	time.Sleep(60 * time.Millisecond)
}

// submit types text and presses Enter.
func (s *term) submit(text string) {
	s.t.Helper()
	s.typ(text)
	s.keys("Enter")
}

// screen is the visible screen.
func (s *term) screen() string {
	out, _ := exec.Command("tmux", "-L", s.socket, "capture-pane", "-p", "-t", "0").Output()
	return string(out)
}

// all is the scrollback plus the screen.
func (s *term) all() string {
	out, _ := exec.Command("tmux", "-L", s.socket, "capture-pane", "-p", "-J", "-S", "-", "-t", "0").Output()
	return string(out)
}

const waitTimeout = 20 * time.Second

func (s *term) poll(desc string, cond func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out waiting for %s\nscreen:\n%s", desc, s.screen())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitFor waits until the scrollback or screen contains want.
func (s *term) waitFor(want string) {
	s.t.Helper()
	s.poll(fmt.Sprintf("%q", want), func() bool { return strings.Contains(s.all(), want) })
}

// waitScreen waits until the visible screen contains want.
func (s *term) waitScreen(want string) {
	s.t.Helper()
	s.poll(fmt.Sprintf("%q on screen", want), func() bool { return strings.Contains(s.screen(), want) })
}

// waitGone waits until the visible screen no longer contains text.
func (s *term) waitGone(text string) {
	s.t.Helper()
	s.poll(fmt.Sprintf("%q to disappear", text), func() bool { return !strings.Contains(s.screen(), text) })
}

// title is the terminal title agenttui set (OSC 0/2).
func (s *term) title() string {
	out, _ := exec.Command("tmux", "-L", s.socket, "display-message", "-p", "-t", "0", "#{pane_title}").Output()
	return strings.TrimSpace(string(out))
}

// busy reports whether the title shows the working spinner (tmux prints
// the braille frame as "_") or "Action Required".
func (s *term) busy() bool {
	t := s.title()
	if strings.Contains(t, "Action Required") || strings.HasPrefix(t, "_ ") || t == "_" {
		return true
	}
	r := []rune(t)
	return len(r) > 0 && r[0] >= 0x2800 && r[0] <= 0x28ff
}

// waitIdle waits for the running turn to end and nothing to wait for input.
func (s *term) waitIdle() {
	s.t.Helper()
	idle := 0
	s.poll("the turn to end", func() bool {
		if s.busy() {
			idle = 0
			return false
		}
		idle++
		return idle >= 3
	})
}

// turn submits a prompt, waits for want, then for the turn to end.
func (s *term) turn(prompt, want string) {
	s.t.Helper()
	s.submit(prompt)
	s.waitFor(want)
	s.waitIdle()
}

// running waits until a turn is in progress.
func (s *term) running() {
	s.t.Helper()
	s.poll("a turn to start", s.busy)
}

// count is how often re matches the scrollback plus screen.
func (s *term) count(re string) int {
	return len(regexp.MustCompile(re).FindAllStringIndex(s.all(), -1))
}

func (s *term) notContains(text string) {
	s.t.Helper()
	if strings.Contains(s.all(), text) {
		s.t.Errorf("unexpected %q in\n%s", text, s.screen())
	}
}

// composerLine is the text after the "› " prompt in the composer.
func (s *term) composerLine() string {
	lines := strings.Split(s.screen(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); strings.HasPrefix(t, "›") {
			return strings.TrimSpace(strings.TrimPrefix(t, "›"))
		}
	}
	return ""
}

func (s *term) resize(w, h int) {
	s.t.Helper()
	s.tmux("resize-window", "-t", "0", "-x", fmt.Sprint(w), "-y", fmt.Sprint(h))
}

// exited reports whether agenttui has exited.
func (s *term) exited() bool {
	out, _ := exec.Command("tmux", "-L", s.socket, "display-message", "-p", "-t", "0", "#{pane_dead}").Output()
	return strings.TrimSpace(string(out)) == "1"
}

// exitStatus is agenttui's exit code once it has exited.
func (s *term) exitStatus() string {
	out, _ := exec.Command("tmux", "-L", s.socket, "display-message", "-p", "-t", "0", "#{pane_dead_status}").Output()
	return strings.TrimSpace(string(out))
}

func (s *term) waitExit() {
	s.t.Helper()
	s.poll("agenttui to exit", s.exited)
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

func echoAgent(t *testing.T) string {
	p, err := filepath.Abs("../examples/echo_agent.py")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// gapBelow is the number of blank rows under the last non-blank row of the
// visible screen.
func (s *term) gapBelow() int {
	lines := strings.Split(strings.TrimSuffix(s.screen(), "\n"), "\n")
	gap := 0
	for i := len(lines) - 1; i >= 0 && strings.TrimSpace(lines[i]) == ""; i-- {
		gap++
	}
	return gap
}

// altScreen reports whether the alternate screen is active.
func (s *term) altScreen() bool {
	out, _ := exec.Command("tmux", "-L", s.socket, "display-message", "-p", "-t", "0", "#{alternate_on}").Output()
	return strings.TrimSpace(string(out)) == "1"
}

// waitPinned waits until the footer is on the last row (no blank rows
// below the bottom pane).
func (s *term) waitPinned(what string) {
	s.t.Helper()
	s.poll("the composer pinned to the bottom "+what, func() bool { return s.gapBelow() == 0 })
}
