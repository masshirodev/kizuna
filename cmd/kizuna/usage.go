package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type usageWindow struct {
	Percent int
	Known   bool
	// Resets is when the window rolls over, zero when the provider did not say.
	// The percentage alone cannot tell a quota about to refill from one that has
	// to last the rest of the week.
	Resets time.Time
	// Stale marks a window the cache did report, but whose figure has gone out
	// of date: the window has rolled over since the reading was taken, so it
	// has probably refilled. A stale window is never Known, and Resets then
	// holds the rollover that already passed (zero when the provider gave none
	// and the age of the reading alone settled it). Readers that only show
	// current figures (the TUI) treat it as unknown; `kizuna usage` says so.
	Stale bool
}

type usageRemaining struct {
	FiveHour usageWindow
	Weekly   usageWindow
	// FetchedAt is when the provider took the reading, zero when it did not
	// say. The cache is refreshed only while a session runs on the profile, so
	// an idle profile's reading can be weeks old.
	FetchedAt time.Time
}

func (usage usageRemaining) known() bool {
	return usage.FiveHour.Known || usage.Weekly.Known
}

func (usage usageRemaining) stale() bool {
	return usage.FiveHour.Stale || usage.Weekly.Stale
}

// read reports whether the cache held any reading for the profile at all,
// current or stale.
func (usage usageRemaining) read() bool {
	return usage.known() || usage.stale()
}

const (
	fiveHourLength = 5 * time.Hour
	weeklyLength   = 7 * 24 * time.Hour
)

// windowFigure turns one cached window into what is left of it now. A window
// whose reset has passed since the reading is stale, never the old figure. So
// is one with no reset time whose reading is older than the window itself:
// it has rolled over at least once since, whatever its reset was.
func windowFigure(used *float64, resets, fetchedAt, now time.Time, length time.Duration) usageWindow {
	if !resets.IsZero() && !now.Before(resets) {
		return usageWindow{Stale: true, Resets: resets}
	}
	if used == nil {
		return usageWindow{}
	}
	if resets.IsZero() && !fetchedAt.IsZero() && now.Sub(fetchedAt) >= length {
		return usageWindow{Stale: true}
	}
	remaining := remainingFromUsed(*used)
	remaining.Resets = resets
	return remaining
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
	var newestStale usageRemaining
	for _, file := range files {
		usage, found := codexUsageFromSession(file.path, file.modTime, now)
		if !found {
			continue
		}
		if usage.known() {
			// One rate-limit event carries every window currently applicable to
			// the account. Older session files may contain obsolete plan limits.
			return usage
		}
		if !newestStale.read() {
			newestStale = usage
		}
	}
	// No file holds a current figure. The newest stale reading is the honest
	// answer: older files only hold older readings of the same windows.
	return newestStale
}

type codexRateLimitWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes int      `json:"window_minutes"`
	ResetsAt      int64    `json:"resets_at"`
}

// codexUsageFromSession reads the rate-limit events of one session file. Each
// event's own timestamp dates the reading; a file whose events carry none is
// dated by its modification time, the last moment Codex wrote to it.
func codexUsageFromSession(path string, modTime, now time.Time) (usageRemaining, bool) {
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
			Timestamp string `json:"timestamp"`
			Payload   struct {
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
		fetchedAt := modTime
		if stamp, err := time.Parse(time.RFC3339Nano, event.Timestamp); err == nil {
			fetchedAt = stamp
		}
		remaining := codexWindowsRemaining(windows, fetchedAt, now)
		// A later event supersedes an earlier one, except that a stale window
		// never displaces a current figure.
		took := false
		if remaining.FiveHour.Known || remaining.FiveHour.Stale && !latest.FiveHour.Known {
			latest.FiveHour, took = remaining.FiveHour, true
		}
		if remaining.Weekly.Known || remaining.Weekly.Stale && !latest.Weekly.Known {
			latest.Weekly, took = remaining.Weekly, true
		}
		if took {
			latest.FetchedAt = fetchedAt
		}
	}
	return latest, latest.read()
}

func codexWindowsRemaining(windows []*codexRateLimitWindow, fetchedAt, now time.Time) usageRemaining {
	var result usageRemaining
	for _, window := range windows {
		if window == nil || window.UsedPercent == nil {
			continue
		}
		var resets time.Time
		if window.ResetsAt > 0 {
			resets = time.Unix(window.ResetsAt, 0)
		}
		switch window.WindowMinutes {
		case 300:
			result.FiveHour = mostConstrained(result.FiveHour, windowFigure(window.UsedPercent, resets, fetchedAt, now, fiveHourLength))
		case 10080:
			result.Weekly = mostConstrained(result.Weekly, windowFigure(window.UsedPercent, resets, fetchedAt, now, weeklyLength))
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
			FetchedAtMs int64 `json:"fetchedAtMs"`
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
	var result usageRemaining
	if ms := state.CachedUsage.FetchedAtMs; ms > 0 {
		result.FetchedAt = time.UnixMilli(ms)
	}
	windowRemaining := func(window limit, length time.Duration) usageWindow {
		var resets time.Time
		if window.ResetsAt != "" {
			if reset, err := time.Parse(time.RFC3339Nano, window.ResetsAt); err == nil {
				resets = reset
			}
		}
		used := window.Percent
		if used == nil {
			used = window.Utilization
		}
		return windowFigure(used, resets, result.FetchedAt, now, length)
	}

	limits := state.CachedUsage.Utilization.Limits
	if len(limits) == 0 {
		result.FiveHour = windowRemaining(state.CachedUsage.Utilization.FiveHour, fiveHourLength)
		result.Weekly = windowRemaining(state.CachedUsage.Utilization.SevenDay, weeklyLength)
		return result
	}
	for _, window := range limits {
		switch {
		case window.Group == "session" || window.Kind == "session":
			result.FiveHour = mostConstrained(result.FiveHour, windowRemaining(window, fiveHourLength))
		case window.Group == "weekly" || window.Kind == "weekly_all":
			result.Weekly = mostConstrained(result.Weekly, windowRemaining(window, weeklyLength))
		}
	}
	return result
}

// mostConstrained keeps the tighter of two readings of the same window. A
// current figure always beats a stale one, and a stale one beats nothing, so
// an out-of-date window is reported as such rather than as unknown.
func mostConstrained(current, candidate usageWindow) usageWindow {
	switch {
	case candidate.Known:
		if current.Known && current.Percent <= candidate.Percent {
			return current
		}
		return candidate
	case current.Known || current.Stale:
		return current
	default:
		return candidate
	}
}

func remainingFromUsed(used float64) usageWindow {
	remaining := int(math.Round(100 - min(max(used, 0), 100)))
	return usageWindow{Percent: remaining, Known: true}
}

// formatReset says when a quota window rolls over, in the shortest form that is
// still unambiguous: a clock time for today, a weekday for later in the week,
// and a date beyond that. An unknown reset renders as nothing rather than as a
// placeholder, because the percentage beside it is the useful half.
func formatReset(now, resets time.Time) string {
	if resets.IsZero() || !resets.After(now) {
		return ""
	}
	local := resets.Local()
	switch delta := local.Sub(now); {
	case delta < 24*time.Hour && local.Day() == now.Day():
		return "resets " + local.Format("15:04")
	case delta < 6*24*time.Hour:
		return "resets " + strings.ToLower(local.Format("Mon"))
	default:
		// The weekday reads as a word and stays lowercase with the rest of the
		// label; a month abbreviation lowercased just looks like a typo.
		return "resets " + local.Format("2 Jan")
	}
}
