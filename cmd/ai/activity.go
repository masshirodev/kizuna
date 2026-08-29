package main

import (
	"os"
	"path/filepath"
	"time"
)

// activityHours is one bucket per hour of the last day, oldest first, so the
// last bucket is always the hour in progress.
const activityHours = 24

// activityScanLimit bounds how many session logs are stat-ed per profile. The
// histogram is redrawn on every refresh and a busy account accumulates
// thousands of transcripts; a capped scan under-counts a very old bucket at
// worst, which costs a bar height and never a wrong shape at the recent end.
const activityScanLimit = 400

// activity is how much a profile was used over the last day, measured by when
// its session logs were last written.
//
// This is not quota. No provider records how much of a limit was spent at a
// given hour, and neither does ai-session, so the honest thing to draw is what
// can actually be counted: sessions touched per hour.
type activity struct {
	counts [activityHours]int
	total  int
	peak   time.Time
}

func (a activity) known() bool { return a.total > 0 }

// profileActivity buckets a profile's session logs by the hour they were last
// written. Only modification times are read — no transcript is opened — so this
// stays cheap enough to run beside the usage refresh.
func profileActivity(profile Profile, now time.Time) activity {
	var result activity
	hour := now.Truncate(time.Hour)
	oldest := hour.Add(-(activityHours - 1) * time.Hour)
	for index, path := range sessionLogPaths(profile) {
		if index >= activityScanLimit {
			break
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		written := info.ModTime()
		if written.Before(oldest) || written.After(now.Add(time.Hour)) {
			continue
		}
		bucket := int(written.Truncate(time.Hour).Sub(oldest) / time.Hour)
		if bucket < 0 || bucket >= activityHours {
			continue
		}
		result.counts[bucket]++
		result.total++
	}
	if result.total > 0 {
		peak := 0
		for bucket, count := range result.counts {
			if count > result.counts[peak] {
				peak = bucket
			}
		}
		result.peak = oldest.Add(time.Duration(peak) * time.Hour)
	}
	return result
}

// sessionLogPaths lists the transcripts a provider writes for a profile. Both
// supported providers keep one file per conversation, which makes the file
// count a usable proxy for how busy the account was.
func sessionLogPaths(profile Profile) []string {
	switch profile.Provider {
	case "codex":
		root, err := profileRoot()
		if err != nil {
			return nil
		}
		return codexRollouts(filepath.Join(root, profile.Name, "codex", "sessions"))
	case "claude":
		return claudeTranscripts(profile)
	default:
		return nil
	}
}

// sparkLevels runs from the shortest tick to a full cell. There is no blank
// level: an empty hour still draws the lowest tick, so the row reads as a
// baseline with peaks rather than as a broken string of gaps.
const sparkLevels = "▁▂▃▄▅▆▇█"

func sparkline(counts []int) string {
	levels := []rune(sparkLevels)
	peak := 0
	for _, count := range counts {
		peak = max(peak, count)
	}
	line := make([]rune, 0, len(counts))
	for _, count := range counts {
		index := 0
		if count > 0 && peak > 0 {
			// Any activity at all clears the baseline, so a single session in an
			// otherwise quiet hour is visible rather than rounded away.
			index = 1 + (count-1)*(len(levels)-2)/max(peak-1, 1)
			index = min(index, len(levels)-1)
		}
		line = append(line, levels[index])
	}
	return string(line)
}
