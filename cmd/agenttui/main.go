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
	"time"

	"agenttui/backend/mock"
	"agenttui/backend/stdio"
	"agenttui/config"
	"agenttui/protocol"
	"agenttui/tui"
)

func main() {
	// Split off the backend command after "--" before flag parsing, so
	// "resume" and stray words can be told apart from the backend argv.
	args, backendArgv := os.Args[1:], []string(nil)
	for i, a := range args {
		if a == "--" {
			args, backendArgv = args[:i], args[i+1:]
			break
		}
	}
	resume := false

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
	configPath := fs.String("config", config.Path(), "settings file (status line, title, keymap)")
	// "resume" may come before or after the flags: agenttui -sessions X resume
	for {
		_ = fs.Parse(args)
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		if rest[0] == "resume" && !resume {
			resume, args = true, rest[1:]
			continue
		}
		usageErr(fs, fmt.Sprintf("unexpected argument %q (put the backend command after --)", rest[0]))
	}
	switch {
	case (*last || *id != "") && !resume:
		usageErr(fs, "-last and -id are only valid with resume")
	case *last && *id != "":
		usageErr(fs, "use either -last or -id, not both")
	case len(backendArgv) > 0 && (*resetDemo || flagSet(fs, "sessions")):
		usageErr(fs, "-reset-demo and -sessions apply to the built-in mock backend, not to a -- backend")
	case len(backendArgv) == 0 && *logPath != "":
		usageErr(fs, "-log captures a -- backend's stderr; the mock backend has none")
	}

	cfg, err := config.Load(*configPath)
	if err == nil {
		err = tui.ValidateConfig(cfg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "agenttui: %v\nFix the config and retry.\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()

	var be protocol.Backend
	var child *stdio.Backend
	if rest := backendArgv; len(rest) > 0 {
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
		be, child = b, b
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

	err = tui.Run(ctx, tui.Options{
		Backend:           be,
		ShowReasoning:     *showReasoning,
		SteerWhileRunning: *steer,
		Notify:            *notify,
		NoReflow:          *noReflow,
		ResumePicker:      resume && !*last && *id == "",
		ResumeLast:        resume && *last,
		ResumeID:          *id,
		Config:            cfg,
		ConfigPath:        *configPath,
	})
	if child != nil {
		// Deliver shutdown and give the agent time to exit cleanly before
		// SIGTERM/SIGKILL (it would otherwise be killed with the UI).
		if cerr := child.Close(3 * time.Second); cerr != nil {
			fmt.Fprintln(os.Stderr, cerr)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usageErr(fs *flag.FlagSet, msg string) {
	fmt.Fprintln(os.Stderr, "agenttui:", msg)
	fs.Usage()
	os.Exit(2)
}

func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
