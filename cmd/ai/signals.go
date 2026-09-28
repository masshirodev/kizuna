package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// signalForwarder keeps the launcher alive for as long as its child, so the
// deferred unlock (and, for OpenCode, the merge of the instance's private store
// back into the profile) runs however the launch ends.
//
// Without it, SIGTERM or SIGHUP kills `ai` on the spot: the child is orphaned
// and keeps running, and the instance is only reclaimed by some later `ai`
// start. That makes the launcher's pid useless as a handle for anything that
// runs sessions headless -- a script that starts `ai run … &` and later kills
// that pid stops nothing.
//
// It is created before the lock is taken, not when the child starts: seeding
// an OpenCode instance copies the profile's opencode.db, which takes seconds
// for a store of a few GB, and a signal in that window used to kill `ai`
// mid-seed. One that arrives before the child exists is held and delivered
// the moment the child is attached, so the child starts, stops at once, and
// the normal exit path merges and unlocks.
//
// SIGTERM and SIGHUP are passed on. SIGINT is only swallowed: a Ctrl-C at a
// terminal already reaches the child through the foreground process group,
// and forwarding it as well would deliver it twice.
type signalForwarder struct {
	signals chan os.Signal
	done    chan struct{}

	mu      sync.Mutex
	process *os.Process
	pending []os.Signal
}

func forwardSignals() *signalForwarder {
	f := &signalForwarder{
		signals: make(chan os.Signal, 4),
		done:    make(chan struct{}),
	}
	signal.Notify(f.signals, syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
	go f.loop()
	return f
}

func (f *signalForwarder) loop() {
	for {
		select {
		case received := <-f.signals:
			if received == os.Interrupt {
				continue
			}
			f.mu.Lock()
			if f.process == nil {
				f.pending = append(f.pending, received)
			} else {
				_ = f.process.Signal(received)
			}
			f.mu.Unlock()
		case <-f.done:
			return
		}
	}
}

// attach names the started child and delivers anything held for it.
func (f *signalForwarder) attach(process *os.Process) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.process = process
	for _, held := range f.pending {
		_ = process.Signal(held)
	}
	f.pending = nil
}

// stop restores default signal handling. Call it once the child has been
// waited for, and after the unlock has run.
func (f *signalForwarder) stop() {
	signal.Stop(f.signals)
	close(f.done)
}
