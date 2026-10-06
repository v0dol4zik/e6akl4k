package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	errMusicNotRecognized     = errors.New("music was not recognized")
	errRecognitionUnavailable = errors.New("music recognition dependencies are unavailable")
	errRecognitionFailed      = errors.New("music recognition service failed")
	errInvalidVoiceAudio      = errors.New("voice audio cannot be decoded")
)

//go:embed shazam_recognize.py
var shazamScript string

type recognizedTrack struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

type musicRecognizer interface {
	recognize(context.Context, string) (recognizedTrack, error)
}

type shazamRecognizer struct {
	python string
	ffmpeg string
}

func (r *shazamRecognizer) recognize(ctx context.Context, path string) (recognizedTrack, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Normalize only a bounded sample locally. Shazam receives its fingerprint, not a media URL.
	wav := filepath.Join(filepath.Dir(path), "sample.wav")
	defer os.Remove(wav)
	_, err := runRecognitionCommand(ctx, r.ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-protocol_whitelist", "file,pipe", "-i", path, "-map", "0:a:0", "-t", "60",
		"-threads", "1", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-f", "wav", wav)
	if err != nil {
		if ctx.Err() != nil {
			return recognizedTrack{}, ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return recognizedTrack{}, errRecognitionUnavailable
		}
		return recognizedTrack{}, errInvalidVoiceAudio
	}
	output, err := runRecognitionCommand(ctx, r.python, "-c", shazamScript, wav)
	if err != nil {
		if ctx.Err() != nil {
			return recognizedTrack{}, ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return recognizedTrack{}, errRecognitionUnavailable
		}
		return recognizedTrack{}, errRecognitionFailed
	}
	var result struct {
		Status string `json:"status"`
		recognizedTrack
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return recognizedTrack{}, errRecognitionFailed
	}
	switch result.Status {
	case "not_found":
		return recognizedTrack{}, errMusicNotRecognized
	case "unavailable":
		return recognizedTrack{}, errRecognitionUnavailable
	case "timeout":
		return recognizedTrack{}, context.DeadlineExceeded
	case "ok":
		result.Title = shortenRunes(strings.TrimSpace(result.Title), maxTitleLength)
		result.Artist = shortenRunes(strings.TrimSpace(result.Artist), maxTitleLength)
		if result.Title != "" {
			return result.recognizedTrack, nil
		}
	}
	return recognizedTrack{}, errRecognitionFailed
}

// Killing the process group also stops ffmpeg or any other helper started by Python.
func runRecognitionCommand(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	stdout := &limitedBuffer{limit: 16 * 1024}
	stderr := &limitedBuffer{limit: 4 * 1024}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return stdout.buf.Bytes(), err
}
