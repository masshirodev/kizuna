//go:build unix

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// startSleeper starts a child that would outlive the test unless something
// signals it.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// waitExit reports how the child ended, or fails if it is still running.
func waitExit(t *testing.T, cmd *exec.Cmd, within time.Duration) error {
	t.Helper()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		return err
	case <-time.After(within):
		t.Fatalf("child still running %s after the signal", within)
		return nil
	}
}

func signalledBy(err error) syscall.Signal {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0
	}
	return status.Signal()
}

func TestForwardSignalsPassesTermAndHupToTheChild(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			signals := forwardSignals()
			defer signals.stop()
			cmd := startSleeper(t)
			signals.attach(cmd.Process)

			// Sent to ourselves, as `kill <ai pid>` would: the launcher must
			// survive it and the child must receive it.
			if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
				t.Fatalf("signal self: %v", err)
			}
			if got := signalledBy(waitExit(t, cmd, 5*time.Second)); got != sig {
				t.Fatalf("child ended by %v, want %v", got, sig)
			}
		})
	}
}

func TestForwardSignalsSwallowsInterrupt(t *testing.T) {
	signals := forwardSignals()
	defer signals.stop()
	cmd := startSleeper(t)
	signals.attach(cmd.Process)

	// A terminal delivers Ctrl-C to the child itself; the launcher must
	// neither die of it nor send a second one.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("signal self: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("child was signalled by an interrupt meant for the terminal: %v", err)
	}
}

// The window that mattered in practice: `ai` seeding a multi-GB opencode.db
// before any child exists. A stop sent then must neither kill the launcher
// (its unlock and merge would never run) nor be lost.
func TestForwardSignalsHoldsAStopThatArrivesBeforeTheChild(t *testing.T) {
	signals := forwardSignals()
	defer signals.stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal self: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // still "seeding": nothing attached

	cmd := startSleeper(t)
	signals.attach(cmd.Process)
	if got := signalledBy(waitExit(t, cmd, 5*time.Second)); got != syscall.SIGTERM {
		t.Fatalf("child ended by %v, want the held SIGTERM", got)
	}
}
