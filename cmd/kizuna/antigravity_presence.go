package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// While agy has a conversation open it holds an exclusive flock on
// presence/<conversation-id>.lock beside its conversations. That is how this
// launcher tells which conversation each running instance has open, and which
// conversations must not be opened a second time: agy itself does not refuse
// it (on 2026-10-27, agy 1.4.7 ran a turn on a conversation another process
// held), and two processes appending steps to one conversation interleave
// them.
//
// The locks are read from /proc/locks rather than probed with a flock of our
// own, because a probe would hold the lock for a moment, and an agy opening
// that conversation at that moment would find it taken. Off Linux there is no
// /proc/locks, so nothing reads as held and nothing is refused.

// antigravityHeldConversations maps each conversation an agy process has open
// to the PID holding it.
func antigravityHeldConversations(profile Profile) map[string]int {
	home, err := antigravityHome(profile)
	if err != nil {
		return nil
	}
	locks, err := os.ReadFile("/proc/locks")
	if err != nil {
		return nil
	}
	return heldPresenceLocks(filepath.Join(home, "presence"), parseProcLocks(string(locks)))
}

// heldPresenceLocks matches the presence files against the held locks by
// device and inode, which is all /proc/locks says about a file.
func heldPresenceLocks(dir string, holders map[string]int) map[string]int {
	entries, err := os.ReadDir(dir)
	if err != nil || len(holders) == 0 {
		return nil
	}
	held := map[string]int{}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".lock")
		if !ok || entry.IsDir() {
			continue
		}
		key, ok := lockFileKey(filepath.Join(dir, entry.Name()))
		if !ok {
			continue
		}
		if pid, ok := holders[key]; ok {
			held[id] = pid
		}
	}
	return held
}

// parseProcLocks reads the flocks out of /proc/locks, keyed the way it names
// a file ("maj:min:inode", the device in hex), with the holder's PID. A line
// with "->" is a process waiting for a lock, not holding it.
func parseProcLocks(text string) map[string]int {
	holders := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[1] != "FLOCK" {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil {
			continue
		}
		holders[fields[5]] = pid
	}
	return holders
}

func lockFileKey(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	dev := uint64(stat.Dev)
	// The kernel's own split of a dev_t into major and minor.
	major := (dev>>8)&0xfff | (dev>>32)&^uint64(0xfff)
	minor := dev&0xff | (dev>>12)&^uint64(0xff)
	return fmt.Sprintf("%02x:%02x:%d", major, minor, stat.Ino), true
}

// antigravityConversationHolder reports the PID of the agy that has a
// conversation open, or zero when none has.
func antigravityConversationHolder(profile Profile, id string) int {
	return antigravityHeldConversations(profile)[id]
}

// errAntigravityConversationOpen is the refusal for opening a conversation
// another agy already has open.
func errAntigravityConversationOpen(pid int) error {
	return fmt.Errorf("that conversation is open in a running agy (PID %d), and two agy processes on one conversation interleave its steps; close it there first", pid)
}

// describeAntigravityInstances names the conversation each instance has open:
// the one whose presence lock is held by the instance's process or one of its
// descendants. The nearest instance up the holder's process chain wins, so an
// agy started inside another instance (a shell in a Claude session, say) is
// credited to its own launch and not to the session around it.
func describeAntigravityInstances(profile Profile, instances []profileInstance) {
	held := antigravityHeldConversations(profile)
	if len(held) == 0 {
		return
	}
	byPID := map[int]int{}
	for index, instance := range instances {
		if instance.pid > 0 {
			byPID[instance.pid] = index
		}
	}
	titles := map[string]string{}
	if records, err := antigravitySessions(profile); err == nil {
		for _, record := range records {
			titles[record.session.id] = record.session.title
		}
	}
	for id, holder := range held {
		for current, steps := holder, 0; current > 0 && steps < 64; steps++ {
			if index, ok := byPID[current]; ok {
				if instances[index].session.id == "" {
					instances[index].session = instanceSession{id: id, title: titles[id]}
				}
				break
			}
			parent, ok := parentPID(current)
			if !ok {
				break
			}
			current = parent
		}
	}
}
