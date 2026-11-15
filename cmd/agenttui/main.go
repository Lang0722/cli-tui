// Command agenttui runs the TUI against either the built-in mock backend or
// an external agent loop speaking the JSONL protocol over stdio.
//
//	agenttui                                  # new session, mock backend
//	agenttui resume                           # pick a saved session
//	agenttui resume -last                     # resume the most recent one
//	agenttui resume -id 20261115-ab12cd34     # resume a specific one
//	agenttui -- python3 my_agent.py --flag    # your agent loop as a subprocess
//	agenttui resume -- python3 my_agent.py    # ...with resume (if it supports it)
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"agenttui/backend/mock"
	"agenttui/backend/stdio"
	"agenttui/protocol"
	"agenttui/tui"
)

func main() {
	args := os.Args[1:]
	resume := false
	if len(args) > 0 && args[0] == "resume" {
		resume = true
		args = args[1:]
	}

	fs := flag.NewFlagSet("agenttui", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: agenttui [resume [-last | -id ID]] [flags] [-- backend-command args...]\n\n")
		fs.PrintDefaults()
	}
	showReasoning := fs.Bool("show-reasoning", false, "print reasoning summaries into the transcript")
	steer := fs.Bool("steer", false, "send input typed while a turn runs immediately instead of queueing it")
	notify := fs.Bool("notify", true, "bell + OSC 777 notification when a turn ends while unfocused")
	logPath := fs.String("log", "", "file for backend stderr (default: discard)")
	sessionsDir := fs.String("sessions", mock.DefaultDir(), "mock backend: directory with saved sessions")
	resetDemo := fs.Bool("reset-demo", false, "mock backend: delete saved sessions and restore the demo history")
	noReflow := fs.Bool("no-reflow", false, "don't clear and re-emit the transcript when the terminal width changes")
	last := fs.Bool("last", false, "with resume: resume the most recent session without the picker")
	id := fs.String("id", "", "with resume: resume this session id")
	_ = fs.Parse(args)

	if (*last || *id != "") && !resume {
		fmt.Fprintln(os.Stderr, "-last and -id are only valid after `resume`")
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()

	var be protocol.Backend
	if rest := fs.Args(); len(rest) > 0 {
		var logw io.Writer = io.Discard
		if *logPath != "" {
			f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			defer f.Close()
			logw = f
		}
		b, err := stdio.Start(ctx, logw, rest[0], rest[1:]...)
		if err != nil {
			fmt.Fprintln(os.Stderr, "start backend:", err)
			os.Exit(1)
		}
		be = b
	} else {
		if *resetDemo {
			if err := mock.ResetDemo(*sessionsDir); err != nil {
				fmt.Fprintln(os.Stderr, "reset demo:", err)
				os.Exit(1)
			}
		}
		b, err := mock.New(*sessionsDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		be = b
	}

	err := tui.Run(ctx, tui.Options{
		Backend:           be,
		ShowReasoning:     *showReasoning,
		SteerWhileRunning: *steer,
		Notify:            *notify,
		NoReflow:          *noReflow,
		ResumePicker:      resume && !*last && *id == "",
		ResumeLast:        resume && *last,
		ResumeID:          *id,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
