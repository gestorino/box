// Command backup is the box's backup, run by root on the box: create writes an archive to stdout,
// resume starts the services a stopped create left, recovery-code makes the code that opens a
// backup on another box, restore puts an archive back, and install-build installs another build.
// The logic is internal/backup; this only reads the command line and says what happened.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"concierge/engine/internal/backup"
)

var (
	version  = "dev"
	revision = ""
)

// root is the box's filesystem root.
const root = "/"

const usage = `usage: backup <command>

  create [-stop]                          write a backup to stdout (-stop leaves the services stopped)
  resume                                  start the services a stopped create left
  recovery-code                           make the recovery code that opens backups on another box
  restore -from FILE [-force] [-new-box]  put a backup back (the code is asked for on the terminal)
  install-build -from FILE -for ID        install another build's archive for the backup ID's restore
  -version                                print the build`

func main() {
	// A reader that goes away (the Mac's ssh) makes a write fail with EPIPE instead of killing the
	// command with every service stopped: create's error path starts them again.
	signal.Ignore(syscall.SIGPIPE)
	os.Exit(run(os.Args[1:], os.Getuid(), backup.Exec{}, os.Stdout, os.Stderr, term.IsTerminal(int(os.Stdout.Fd()))))
}

// run is the command: stdoutTTY says whether stdout is a terminal.
func run(args []string, uid int, sys backup.System, stdout, stderr io.Writer, stdoutTTY bool) int {
	if len(args) == 1 && args[0] == "-version" {
		fmt.Fprintf(stdout, "backup %s %s\n", version, revision)
		return 0
	}
	if uid != 0 {
		fmt.Fprintln(stderr, "backup: run as root")
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 1
	}
	if err := dispatch(args[0], args[1:], sys, stdout, stderr, stdoutTTY); err != nil {
		fmt.Fprintln(stderr, "backup:", err)
		return 1
	}
	return 0
}

func dispatch(cmd string, args []string, sys backup.System, stdout, stderr io.Writer, stdoutTTY bool) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch cmd {
	case "create":
		stop := fs.Bool("stop", false, "leave the services stopped")
		if err := fs.Parse(args); err != nil {
			return err
		}
		h, err := backup.Create(sys, root, stdout, *stop)
		if err != nil {
			return err
		}
		line, err := json.Marshal(h)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "%s\n", line)
		if !h.Recovery {
			fmt.Fprintln(stderr, "This backup's secrets can't move to another box until make recovery-code runs.")
		}
		return nil
	case "resume":
		if err := fs.Parse(args); err != nil {
			return err
		}
		if err := backup.Resume(sys, root); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "The core services are running.")
		return nil
	case "recovery-code":
		if err := fs.Parse(args); err != nil {
			return err
		}
		// The code is shown once, on the owner's terminal: never into a file, a pipe or a log.
		if !stdoutTTY {
			return errors.New("the recovery code is shown only on a terminal: run make recovery-code from one")
		}
		code, replaced, err := backup.RecoveryCode(sys, root)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, code)
		if replaced {
			fmt.Fprintln(stdout, "Backups taken before still open with the previous code; delete them if it leaked.")
		}
		return nil
	case "restore":
		from := fs.String("from", "", "the backup file")
		force := fs.Bool("force", false, "restore a backup the box was not stopped for")
		newBox := fs.Bool("new-box", false, "ignore this box's own key; use the recovery code")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *from == "" {
			return errors.New("restore needs -from FILE")
		}
		r, err := backup.Restore(sys, root, *from, backup.RestoreOptions{Force: *force, NewBox: *newBox, ReadCode: func() (string, error) { return readCode(stderr) }})
		if err != nil {
			return err
		}
		if r.SameBox {
			fmt.Fprintln(stdout, "Restored on the same box.")
		} else {
			fmt.Fprintln(stdout, "Restored onto this box with the recovery code; its master key is sealed to this box's TPM.")
		}
		fmt.Fprintln(stdout, "Every core service came back.")
		printLeftovers(stdout, r.Leftovers)
		return nil
	case "install-build":
		from := fs.String("from", "", "the build's archive")
		forID := fs.String("for", "", "the ID of the backup the restore that follows puts back")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *from == "" || *forID == "" {
			return errors.New("install-build needs -from FILE -for ID")
		}
		left, err := backup.InstallBuild(sys, root, *from, *forID)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Installed.")
		printLeftovers(stdout, left)
		return nil
	}
	fmt.Fprintf(stderr, "%s\n", usage)
	return fmt.Errorf("unknown command %q", cmd)
}

func printLeftovers(w io.Writer, left []string) {
	if len(left) == 0 {
		return
	}
	fmt.Fprintf(w, "Old copies the command could not delete; delete them yourself:\n  %s\n", strings.Join(left, "\n  "))
}

// readCode asks for the recovery code on the terminal, without echo.
func readCode(stderr io.Writer) (string, error) {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return "", errors.New("no terminal to ask the recovery code on")
	}
	defer tty.Close()
	fmt.Fprint(stderr, "Recovery code: ")
	b, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(stderr)
	if err != nil {
		return "", errors.New("could not read the recovery code")
	}
	return string(b), nil
}
