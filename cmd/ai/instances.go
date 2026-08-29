package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	instancesDirectory = "instances"
	instanceMetaFile   = "instance.json"
)

type profileInstance struct {
	lockDir string
	pid     int
	// folder is the directory the instance was launched in, recorded so a
	// later hijack can reopen the same conversation from the same place.
	folder  string
	session instanceSession
}

// instanceMeta is the launch detail a lock file cannot carry. It is written
// beside .active.lock so another ai process can describe a running instance
// without asking the provider CLI anything.
type instanceMeta struct {
	Folder  string `json:"folder"`
	Started string `json:"started"`
}

func supportsConcurrentRuns(profile Profile) bool {
	return profile.Provider == "codex" || profile.Provider == "claude"
}

// acquireProfileRunLock gives Codex and Claude each a short-lived instance
// directory while leaving their provider home shared. Providers whose auth
// storage is not concurrency-safe keep the original profile-wide lock.
func acquireProfileRunLock(profile Profile, workdir string) (string, func(), error) {
	if !supportsConcurrentRuns(profile) {
		return acquireExclusiveRunLock(workdir)
	}
	return acquireProfileInstance(workdir)
}

func acquireExclusiveRunLock(workdir string) (string, func(), error) {
	unlock, err := acquireProfileLock(workdir)
	return workdir, unlock, err
}

// acquireProfileLock is the exclusive side of the profile lock. Login,
// integration, export, and providers without concurrent auth support use it.
func acquireProfileLock(workdir string) (func(), error) {
	unlock, err := acquireProcessLock(workdir)
	if err != nil {
		return nil, err
	}
	instances, err := activeProfileInstanceLocks(workdir)
	if err != nil || len(instances) > 0 {
		unlock()
		if err != nil {
			return nil, err
		}
		return nil, profileBusyError(workdir)
	}
	return unlock, nil
}

func acquireProfileInstance(workdir string) (string, func(), error) {
	root := filepath.Join(workdir, instancesDirectory)
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", nil, err
	}
	stagingDir, err := os.MkdirTemp(root, ".creating-")
	if err != nil {
		return "", nil, err
	}
	unlockStaging, err := acquireProcessLock(stagingDir)
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", nil, err
	}
	instanceDir := filepath.Join(root, "run-"+strings.TrimPrefix(filepath.Base(stagingDir), ".creating-"))
	if err := os.Rename(stagingDir, instanceDir); err != nil {
		unlockStaging()
		_ = os.RemoveAll(stagingDir)
		return "", nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(instanceDir)
	}

	// Creating the instance before checking the exclusive lock closes both
	// sides of the race: an exclusive caller will see this instance, while an
	// already-exclusive caller is visible here.
	active, err := activeLock(filepath.Join(workdir, ".active.lock"), true)
	if err != nil || active {
		cleanup()
		if err != nil {
			return "", nil, err
		}
		return "", nil, profileBusyError(workdir)
	}
	return instanceDir, cleanup, nil
}

func acquireProcessLock(workdir string) (func(), error) {
	lockPath := filepath.Join(workdir, ".active.lock")
	for attempt := 0; attempt < 3; attempt++ {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, err
			}
			if err := file.Close(); err != nil {
				_ = os.Remove(lockPath)
				return nil, err
			}
			return func() {
				_ = os.Remove(lockPath)
				_ = os.Remove(filepath.Join(workdir, instanceMetaFile))
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}

		active, inspectErr := profileLockIsActive(lockPath)
		if errors.Is(inspectErr, os.ErrNotExist) {
			continue
		}
		if inspectErr != nil || active {
			return nil, profileBusyError(workdir)
		}
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, profileBusyError(workdir)
}

func profileBusyError(workdir string) error {
	return fmt.Errorf("profile is already running (%s); refusing exclusive access", workdir)
}

// profileLockIsActive reports whether any process recorded in a profile lock
// still exists. Invalid locks are returned as errors so callers fail closed.
func profileLockIsActive(lockPath string) (bool, error) {
	pids, err := profileLockPIDs(lockPath)
	if err != nil {
		return false, err
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			return true, nil
		}
	}
	return false, nil
}

func profileLockPID(lockDir string) (int, error) {
	pids, err := profileLockPIDs(filepath.Join(lockDir, ".active.lock"))
	if err != nil {
		return 0, err
	}
	return pids[len(pids)-1], nil
}

func profileLockPIDs(lockPath string) ([]int, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return nil, errors.New("profile lock is empty")
	}
	pids := make([]int, 0, len(fields))
	for _, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			return nil, errors.New("profile lock has an invalid process PID")
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func activeLock(lockPath string, removeStale bool) (bool, error) {
	active, err := profileLockIsActive(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || active {
		return active, err
	}
	if removeStale {
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func activeProfileInstanceLocks(workdir string) ([]string, error) {
	root := filepath.Join(workdir, instancesDirectory)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var locks []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// A launch writes its PID lock before atomically renaming this staging
		// directory. Ignoring it avoids treating a half-created instance as
		// stale; the creator checks the exclusive lock after the rename.
		if strings.HasPrefix(entry.Name(), ".creating-") {
			continue
		}
		instanceDir := filepath.Join(root, entry.Name())
		active, err := activeLock(filepath.Join(instanceDir, ".active.lock"), false)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if active {
			locks = append(locks, instanceDir)
			continue
		}
		_ = os.RemoveAll(instanceDir)
	}
	sort.Strings(locks)
	return locks, nil
}

func activeProfileLocks(workdir string) ([]string, error) {
	var locks []string
	active, err := activeLock(filepath.Join(workdir, ".active.lock"), true)
	if err != nil {
		return nil, err
	}
	if active {
		locks = append(locks, workdir)
	}
	instances, err := activeProfileInstanceLocks(workdir)
	if err != nil {
		return nil, err
	}
	return append(locks, instances...), nil
}

func activeProfileInstances(profile Profile) ([]profileInstance, error) {
	root, err := profileRoot()
	if err != nil {
		return nil, err
	}
	lockDirs, err := activeProfileLocks(filepath.Join(root, profile.Name))
	if err != nil {
		return nil, err
	}
	instances := make([]profileInstance, 0, len(lockDirs))
	for _, lockDir := range lockDirs {
		pid, err := profileLockPID(lockDir)
		if err != nil {
			return nil, err
		}
		instances = append(instances, profileInstance{
			lockDir: lockDir,
			pid:     pid,
			folder:  readInstanceMeta(lockDir).Folder,
		})
	}
	return instances, nil
}

func profileRunningCount(profile Profile) int {
	root, err := profileRoot()
	if err != nil {
		return 0
	}
	locks, err := activeProfileLocks(filepath.Join(root, profile.Name))
	if err != nil {
		return 1
	}
	return len(locks)
}

func profileIsRunning(profile Profile) bool {
	return profileRunningCount(profile) > 0
}

// setProfileChildPID records the launcher and provider CLI processes. For a
// concurrent launch, workdir is that launch's instance directory.
func setProfileChildPID(workdir string, pid int) error {
	lockPath := filepath.Join(workdir, ".active.lock")
	return os.WriteFile(lockPath, []byte(fmt.Sprintf("%d\n%d\n", os.Getpid(), pid)), 0600)
}

// setProfileInstanceMeta records where a launch happened. A failure here is not
// fatal to the launch itself: the instance simply cannot be described later.
func setProfileInstanceMeta(workdir, folder string) error {
	data, err := json.Marshal(instanceMeta{Folder: folder, Started: time.Now().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workdir, instanceMetaFile), append(data, '\n'), 0600)
}

func readInstanceMeta(workdir string) instanceMeta {
	data, err := os.ReadFile(filepath.Join(workdir, instanceMetaFile))
	if err != nil {
		return instanceMeta{}
	}
	var meta instanceMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return instanceMeta{}
	}
	return meta
}
