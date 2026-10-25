package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVKMusicLinks(t *testing.T) {
	canonical := "https://vk.ru/audio123_456_shared-key"
	for _, raw := range []string{
		"vk.ru/audio123_456_shared-key",
		"https://vk.com/audio123_456_shared-key?from=share",
		"http://m.vk.ru/audio123_456_shared-key/",
		"https://music.vk.com/audio123_456?access_key=shared-key",
	} {
		if got := detectURL(raw); got != canonical {
			t.Errorf("canonical link = %q, want %q", got, canonical)
		}
	}
	album := "https://vk.ru/music/album/-123_456_shared"
	for _, raw := range []string{
		"https://vk.ru/audio_playlist-123_456_shared",
		"https://vk.com/music/album/-123_456_shared",
		"https://m.vk.ru/audio?act=audio_playlist-123_456&access_hash=shared",
		"https://vk.ru/audios123?z=audio_playlist-123_456_shared",
	} {
		if got := detectURL(raw); got != album {
			t.Errorf("album link = %q, want %q", got, album)
		}
	}
	for _, raw := range []string{
		"https://vk.ru.evil.test/audio123_456", "https://vk.ru@evil.test/audio123_456",
		"https://evil.test/vk.ru/audio123_456", "https://vk.ru:443/audio123_456",
		"https://vk.ru/audio0_456", "https://vk.ru/audio123_0",
		"https://vk.ru/audio9223372036854775808_456", "https://vk.ru/audio123_456_other/extra",
		"https://vk.ru/audio123_456_key?access_key=other",
		"https://vk.ru/music/album/-123_456?access_hash=one&access_hash=two",
	} {
		if got := detectURL(raw); got != "" {
			t.Errorf("accepted malformed link %q as %q", raw, got)
		}
	}
	for _, raw := range []string{
		"https://vk.com/video-123_456", "https://vk.ru/wall-123_456",
		"https://vk.com/video-123_456?z=audio_playlist-123_456",
	} {
		if _, ok := parseVKMusicURL(raw); ok {
			t.Errorf("reclassified VK video or wall as music")
		}
		if got := detectURL(raw); got != raw {
			t.Errorf("changed existing VK link")
		}
	}
}

func TestVKMusicTraceHidesAccessKeys(t *testing.T) {
	for _, raw := range []string{
		"https://vk.ru/audio123_456_private-secret",
		"https://vk.com/music/album/-123_456_private-secret?from=share",
		"https://m.vk.ru/audio?act=audio_playlist-123_456&access_hash=private-secret",
	} {
		if got := safeTraceURL(raw); strings.Contains(got, "private-secret") || strings.Contains(got, "access_hash") {
			t.Fatal("trace leaked a VK access key")
		}
	}
}

func TestVKCookieIsolationAndReplacement(t *testing.T) {
	dir := t.TempDir()
	global, vk := filepath.Join(dir, "youtube.txt"), filepath.Join(dir, "vk.txt")
	youtube := "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tyoutube-only\n"
	first := "# Netscape HTTP Cookie File\n#HttpOnly_.vk.ru\tTRUE\t/\tTRUE\t0\tremixsid\tvk-first\n" + strings.SplitN(youtube, "\n", 2)[1]
	for path, data := range map[string]string{global: youtube, vk: first} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	d := &downloader{downloadDir: dir, cookiesFile: global, vkCookiesFile: vk}
	args := []string{"--", "https://vk.ru/audio123_456"}
	path, cleanup, err := d.isolatedCookiesForArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "vk-first") || strings.Contains(string(data), "youtube-only") {
		t.Fatal("VK did not get its own filtered session")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("isolated cookies are not private")
	}
	// An atomic source replacement must not mutate an operation's open snapshot.
	replacement := vk + ".new"
	if err := os.WriteFile(replacement, []byte(strings.ReplaceAll(first, "vk-first", "vk-second")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, vk); err != nil {
		t.Fatal(err)
	}
	second, clearSecond, err := d.isolatedCookiesForArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	defer clearSecond()
	oldData, _ := os.ReadFile(path)
	newData, _ := os.ReadFile(second)
	if !strings.Contains(string(oldData), "vk-first") || !strings.Contains(string(newData), "vk-second") {
		t.Fatal("session replacement was not isolated")
	}
	pathYT, clearYT, err := d.isolatedCookiesForArgs([]string{"--", "https://youtu.be/aaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	defer clearYT()
	ytData, _ := os.ReadFile(pathYT)
	if string(ytData) != youtube {
		t.Fatal("VK session changed YouTube cookies")
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("temporary VK cookies were not removed")
	}
}

func TestVKMissingSessionAndPrivateFailures(t *testing.T) {
	d := &downloader{downloadDir: t.TempDir(), vkCookiesFile: filepath.Join(t.TempDir(), "missing.txt")}
	args := []string{"--", "https://vk.ru/audio123_456"}
	if _, cleanup, err := d.isolatedCookiesForArgs(args); !errors.Is(err, errVKMusicSessionRequired) {
		cleanup()
		t.Fatal("missing VK session was not classified")
	}
	for _, tc := range []struct{ raw, want string }{
		{"ERROR: vk_music:session secret-cookie", errVKMusicSessionRequired.Error()},
		{"ERROR: vk_music:preview_only https://cdn.example/?secret=token", vkMusicErrorMessage("preview_only")},
		{"ERROR: HTTP Error 429 https://vk.ru/?secret=token", vkMusicErrorMessage("rate_limit")},
		{"ERROR: invalid Netscape cookie row: secret-cookie", vkMusicErrorMessage("extract")},
	} {
		message, err := vkMusicProcessError(args, tc.raw, errors.New("exit status 1"))
		if message != tc.want || err == nil || err.Error() != tc.want {
			t.Fatal("unclassified subprocess failure or private error output")
		}
	}
	message, err := vkMusicProcessError(args, "ERROR: vk_music:unavailable secret", nil)
	if err == nil || message != vkMusicErrorMessage("unavailable") {
		t.Fatal("--ignore-errors hid the VK failure")
	}
}

func TestVKMusicGateWaitCanBeCancelled(t *testing.T) {
	var gate vkMusicGate
	release, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { clear, err := gate.acquire(ctx); clear(); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled VK request retained the gate")
	}
	release()
	clear, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clear()
}
