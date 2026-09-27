package main

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	// diskWaitTimeout bounds how long a playlist batch waits for free disk space.
	diskWaitTimeout = 15 * time.Minute
	// diskBatchFactor covers the peak disk use of a batch against its audio: the downloaded
	// tracks, then the archive they are packed into or the copy the local Bot API server keeps
	// while it uploads a file.
	diskBatchFactor = 2
)

// errDiskFull stops a playlist download whose next batch found no free disk space in time.
var errDiskFull = errors.New("not enough free disk space")

// diskBudget holds back playlist batches while the disk shared by the downloads has less free
// space than their estimated size plus a reserve: a batch waits for earlier ones to be sent and
// removed instead of filling the disk of the whole host. Space reserved by running batches
// counts as used, since their files are still being written.
type diskBudget struct {
	mu       sync.Mutex
	reserved int64
	floor    int64
	free     func() (int64, error)
	poll     time.Duration
	timeout  time.Duration
}

func newDiskBudget(free func() (int64, error), floor int64) *diskBudget {
	return &diskBudget{free: free, floor: floor, poll: 5 * time.Second, timeout: diskWaitTimeout}
}

func diskFree(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Clean(path), &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail * uint64(stat.Bsize)), nil
}

// acquire reserves bytes of disk space, waiting until the free space less every reservation still
// leaves the floor. waiting is called once if the batch has to wait. A disk that cannot be read
// does not block downloads. The returned release is safe to call more than once.
func (b *diskBudget) acquire(ctx context.Context, bytes int64, waiting func()) (func(), error) {
	if b == nil || bytes <= 0 {
		return func() {}, nil
	}
	deadline := time.Now().Add(b.timeout)
	notified := false
	for {
		b.mu.Lock()
		free, err := b.free()
		if err != nil || free-b.reserved-bytes >= b.floor {
			b.reserved += bytes
			b.mu.Unlock()
			if err != nil {
				log.Printf("disk space check: %v", err)
			}
			var once sync.Once
			return func() {
				once.Do(func() {
					b.mu.Lock()
					b.reserved -= bytes
					b.mu.Unlock()
				})
			}, nil
		}
		b.mu.Unlock()
		if !time.Now().Before(deadline) {
			return nil, errDiskFull
		}
		if !notified && waiting != nil {
			notified = true
			waiting()
		}
		timer := time.NewTimer(b.poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// estimateSelectionBytes estimates the audio size of the tracks start to end (1-based) of a
// playlist selection: the known length of each track, or an average track without one.
func estimateSelectionBytes(preview mediaPreview, start, end int, format, quality string) int64 {
	var total int64
	for position := start; position <= end; position++ {
		seconds := averageTrackSeconds
		if position >= 1 && position <= len(preview.Tracks) && preview.Tracks[position-1].Seconds > 0 {
			seconds = preview.Tracks[position-1].Seconds
		}
		total += estimateAudioSize(seconds, format, quality)
	}
	return total
}

// playlistZIPBatchSize is the number of tracks downloaded and packed per ZIP batch: as many
// average tracks as fit in playlistZIPBatchBytes, from playlistBatchTracks to
// playlistZIPBatchTracks, so a lossless batch takes about as much disk as an MP3 one.
func playlistZIPBatchSize(format, quality string) int {
	size := int(playlistZIPBatchBytes / estimateAudioSize(averageTrackSeconds, format, quality))
	return min(max(size, playlistBatchTracks), playlistZIPBatchTracks)
}
