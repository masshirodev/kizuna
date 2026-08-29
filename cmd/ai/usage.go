package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type usageWindow struct {
	Percent int
	Known   bool
}

type usageRemaining struct {
	FiveHour usageWindow
	Weekly   usageWindow
}

func (usage usageRemaining) known() bool {
	return usage.FiveHour.Known || usage.Weekly.Known
}

// profileUsageRemaining reads only quota caches written inside the selected
// profile. In particular, it never opens a credential file or combines
// accounts through a shared usage service.
func profileUsageRemaining(profile Profile, now time.Time) usageRemaining {
	root, err := profileRoot()
	if err != nil {
		return usageRemaining{}
	}
	dir := filepath.Join(root, profile.Name)
	switch profile.Provider {
	case "codex":
		return codexUsageRemaining(filepath.Join(dir, "codex", "sessions"), now)
	case "claude":
		return claudeUsageRemaining(filepath.Join(dir, "claude", ".claude.json"), now)
	default:
		return usageRemaining{}
	}
}

func loadProfileUsage(profiles []Profile, now time.Time) map[string]usageRemaining {
	usage := make(map[string]usageRemaining, len(profiles))
	for _, profile := range profiles {
		usage[profile.Name] = profileUsageRemaining(profile, now)
	}
	return usage
}

type codexSessionFile struct {
	path    string
	modTime time.Time
}

func codexUsageRemaining(sessionsDir string, now time.Time) usageRemaining {
	var files []codexSessionFile
	_ = filepath.WalkDir(sessionsDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			files = append(files, codexSessionFile{path: path, modTime: info.ModTime()})
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	for _, file := range files {
		if usage, found := codexUsageFromSession(file.path, now); found {
			// One rate-limit event carries every window currently applicable to
			// the account. Older session files may contain obsolete plan limits.
			return usage
		}
	}
	return usageRemaining{}
}

type codexRateLimitWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes int      `json:"window_minutes"`
	ResetsAt      int64    `json:"resets_at"`
}

func codexUsageFromSession(path string, now time.Time) (usageRemaining, bool) {
	file, err := os.Open(path)
	if err != nil {
		return usageRemaining{}, false
	}
	defer file.Close()

	var latest usageRemaining
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var event struct {
			Payload struct {
				RateLimits *struct {
					Primary         *codexRateLimitWindow `json:"primary"`
					Secondary       *codexRateLimitWindow `json:"secondary"`
					IndividualLimit *codexRateLimitWindow `json:"individual_limit"`
				} `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Payload.RateLimits == nil {
			continue
		}
		windows := []*codexRateLimitWindow{
			event.Payload.RateLimits.Primary,
			event.Payload.RateLimits.Secondary,
			event.Payload.RateLimits.IndividualLimit,
		}
		remaining := codexWindowsRemaining(windows, now)
		if remaining.FiveHour.Known {
			latest.FiveHour = remaining.FiveHour
		}
		if remaining.Weekly.Known {
			latest.Weekly = remaining.Weekly
		}
	}
	return latest, latest.known()
}

func codexWindowsRemaining(windows []*codexRateLimitWindow, now time.Time) usageRemaining {
	var result usageRemaining
	for _, window := range windows {
		if window == nil || window.UsedPercent == nil {
			continue
		}
		if window.ResetsAt > 0 && now.Unix() >= window.ResetsAt {
			continue
		}
		remaining := remainingFromUsed(*window.UsedPercent)
		switch window.WindowMinutes {
		case 300:
			result.FiveHour = mostConstrained(result.FiveHour, remaining)
		case 10080:
			result.Weekly = mostConstrained(result.Weekly, remaining)
		}
	}
	return result
}

func claudeUsageRemaining(path string, now time.Time) usageRemaining {
	data, err := os.ReadFile(path)
	if err != nil {
		return usageRemaining{}
	}
	type limit struct {
		Percent     *float64 `json:"percent"`
		Utilization *float64 `json:"utilization"`
		ResetsAt    string   `json:"resets_at"`
		Kind        string   `json:"kind"`
		Group       string   `json:"group"`
	}
	var state struct {
		CachedUsage struct {
			Utilization struct {
				Limits   []limit `json:"limits"`
				FiveHour limit   `json:"five_hour"`
				SevenDay limit   `json:"seven_day"`
			} `json:"utilization"`
		} `json:"cachedUsageUtilization"`
	}
	if json.Unmarshal(data, &state) != nil {
		return usageRemaining{}
	}
	windowRemaining := func(window limit) usageWindow {
		if window.ResetsAt != "" {
			reset, err := time.Parse(time.RFC3339Nano, window.ResetsAt)
			if err == nil && !now.Before(reset) {
				return usageWindow{}
			}
		}
		used := window.Percent
		if used == nil {
			used = window.Utilization
		}
		if used == nil {
			return usageWindow{}
		}
		return remainingFromUsed(*used)
	}

	var result usageRemaining
	limits := state.CachedUsage.Utilization.Limits
	if len(limits) == 0 {
		result.FiveHour = windowRemaining(state.CachedUsage.Utilization.FiveHour)
		result.Weekly = windowRemaining(state.CachedUsage.Utilization.SevenDay)
		return result
	}
	for _, window := range limits {
		remaining := windowRemaining(window)
		switch {
		case window.Group == "session" || window.Kind == "session":
			result.FiveHour = mostConstrained(result.FiveHour, remaining)
		case window.Group == "weekly" || window.Kind == "weekly_all":
			result.Weekly = mostConstrained(result.Weekly, remaining)
		}
	}
	return result
}

func mostConstrained(current, candidate usageWindow) usageWindow {
	if !candidate.Known || current.Known && current.Percent <= candidate.Percent {
		return current
	}
	return candidate
}

func remainingFromUsed(used float64) usageWindow {
	remaining := int(math.Round(100 - min(max(used, 0), 100)))
	return usageWindow{Percent: remaining, Known: true}
}
