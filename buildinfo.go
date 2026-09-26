package main

import (
	"context"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// buildCommit and buildDate are filled by `git archive` through the export-subst attribute in
// .gitattributes, so an image built from an archive knows its commit without a .git directory.
var (
	buildCommit = "$Format:%h$"
	buildDate   = "$Format:%cs$"
)

// processStarted is the uptime origin shown by the status message.
var processStarted = time.Now()

// buildVersion returns "<commit> (<date>)" for archive builds, the VCS revision embedded by
// `go build` inside a checkout, or "dev".
func buildVersion() string {
	commit, date := buildCommit, buildDate
	if strings.HasPrefix(commit, "$Format") {
		commit, date = "", ""
		if info, ok := debug.ReadBuildInfo(); ok {
			modified := false
			for _, setting := range info.Settings {
				switch setting.Key {
				case "vcs.revision":
					commit = setting.Value
				case "vcs.time":
					date = setting.Value
				case "vcs.modified":
					modified = setting.Value == "true"
				}
			}
			if len(commit) > 7 {
				commit = commit[:7]
			}
			if len(date) > 10 {
				date = date[:10]
			}
			if commit != "" && modified {
				commit += "-dirty"
			}
		}
	}
	if commit == "" {
		return "dev"
	}
	if strings.HasPrefix(date, "$Format") || date == "" {
		return commit
	}
	return commit + " (" + date + ")"
}

var ytdlpVersionCache struct {
	once    sync.Once
	version atomic.Value
}

// ytdlpVersion asks the yt-dlp binary for its version once and may block for a few seconds on
// the first call; an unreadable version is "".
func (d *downloader) ytdlpVersion() string {
	if d == nil || d.bin == "" {
		return ""
	}
	ytdlpVersionCache.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		version := ""
		if output, err := exec.CommandContext(ctx, d.bin, "--version").Output(); err == nil {
			version = strings.TrimSpace(string(output))
		}
		ytdlpVersionCache.version.Store(version)
	})
	return cachedYtdlpVersion()
}

// cachedYtdlpVersion never blocks: it is "" until ytdlpVersion has run once.
func cachedYtdlpVersion() string {
	version, _ := ytdlpVersionCache.version.Load().(string)
	return version
}
