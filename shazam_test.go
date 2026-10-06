package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recognitionTestBin(t *testing.T, name, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestShazamRecognizerHandlesProviderResults(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   recognizedTrack
		err    error
	}{
		{"recognized", `{"status":"ok","title":" Track ","artist":" Artist "}`, recognizedTrack{Title: "Track", Artist: "Artist"}, nil},
		{"unicode", `{"status":"ok","title":"Песня","artist":"Исполнитель"}`, recognizedTrack{Title: "Песня", Artist: "Исполнитель"}, nil},
		{"no match", `{"status":"not_found"}`, recognizedTrack{}, errMusicNotRecognized},
		{"missing dependency", `{"status":"unavailable"}`, recognizedTrack{}, errRecognitionUnavailable},
		{"timeout", `{"status":"timeout"}`, recognizedTrack{}, context.DeadlineExceeded},
		{"service failure", `{"status":"error"}`, recognizedTrack{}, errRecognitionFailed},
		{"empty title", `{"status":"ok","title":" "}`, recognizedTrack{}, errRecognitionFailed},
		{"malformed metadata", `{"status":"ok","title":42}`, recognizedTrack{}, errRecognitionFailed},
		{"invalid JSON", "invalid output", recognizedTrack{}, errRecognitionFailed},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ffmpeg := recognitionTestBin(t, "ffmpeg", "for arg in \"$@\"; do output=\"$arg\"; done\nprintf wav > \"$output\"\n")
			python := recognitionTestBin(t, "python", "printf '%s' '"+strings.ReplaceAll(tt.output, "'", "'\"'\"'")+"'\n")
			r := &shazamRecognizer{python: python, ffmpeg: ffmpeg}
			path := filepath.Join(t.TempDir(), "voice")
			got, err := r.recognize(context.Background(), path)
			if got != tt.want || !errors.Is(err, tt.err) {
				t.Fatalf("recognize()=%#v err=%v, want=%#v err=%v", got, err, tt.want, tt.err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "sample.wav")); !os.IsNotExist(err) {
				t.Fatalf("recognition left its normalized sample: %v", err)
			}
		})
	}
}

func TestShazamRecognizerMissingToolsAndInvalidAudio(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-tool")
	ffmpeg := recognitionTestBin(t, "ffmpeg", "for arg in \"$@\"; do output=\"$arg\"; done\nprintf wav > \"$output\"\n")
	invalid := recognitionTestBin(t, "invalid-ffmpeg", "exit 1\n")
	for _, tt := range []struct {
		name, ffmpeg, python string
		want                 error
	}{
		{"missing ffmpeg", missing, missing, errRecognitionUnavailable},
		{"invalid recording", invalid, missing, errInvalidVoiceAudio},
		{"missing Python", ffmpeg, missing, errRecognitionUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &shazamRecognizer{python: tt.python, ffmpeg: tt.ffmpeg}
			_, err := r.recognize(context.Background(), filepath.Join(t.TempDir(), "voice"))
			if !errors.Is(err, tt.want) {
				t.Fatalf("recognition error=%v want=%v", err, tt.want)
			}
		})
	}
}

func TestRecognitionCommandCancelsChildProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runRecognitionCommand(ctx, "sh", "-c", "sleep 30 & wait")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("recognition helper did not stop its subprocesses: err=%v elapsed=%s", err, time.Since(started))
	}
}

func TestShazamPythonHelperReturnsOnlyMetadata(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is not installed")
	}
	// Stub the provider at its Python boundary; no network or real Shazam library is needed.
	stub := `import asyncio, json, sys, types
sys.version_info = (3, 11, 0)
fixture = json.loads(sys.argv[2])
class Retry:
    def __init__(self, **kwargs):
        assert kwargs["attempts"] == 2
class HTTPClient:
    def __init__(self, retry_options):
        assert isinstance(retry_options, Retry)
    async def request(self, method, url, **kwargs):
        assert kwargs["raise_for_status"] is True
class Shazam:
    def __init__(self, http_client):
        assert isinstance(http_client, HTTPClient)
        self.http_client = http_client
    async def recognize(self, path):
        assert path == "sample.wav"
        await self.http_client.request("POST", "https://shazam.test")
        if isinstance(fixture, dict) and "fixture_error" in fixture:
            error = fixture["fixture_error"]
            if error == "import":
                raise ImportError("missing library")
            if error == "timeout":
                raise TimeoutError("provider timeout")
            raise RuntimeError("https://provider.test/?token=secret recording.wav")
        return fixture
sys.modules["shazamio"] = types.SimpleNamespace(Shazam=Shazam, HTTPClient=HTTPClient)
sys.modules["aiohttp_retry"] = types.SimpleNamespace(ExponentialRetry=Retry)
script = sys.argv[1]
sys.argv = ["recognizer", "sample.wav"]
exec(script, {"__name__": "__main__"})
`
	for _, tt := range []struct {
		name, fixture, status string
	}{
		{"track", `{"track":{"title":"Песня","subtitle":"Исполнитель","url":"https://provider.test/?token=secret"}}`, "ok"},
		{"silence", `{"matches":[]}`, "not_found"},
		{"bad result", `[]`, "error"},
		{"bad title", `{"track":{"title":42}}`, "error"},
		{"provider failure", `{"fixture_error":"error"}`, "error"},
		{"missing dependency", `{"fixture_error":"import"}`, "unavailable"},
		{"deadline", `{"fixture_error":"timeout"}`, "timeout"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			output, err := runRecognitionCommand(ctx, python, "-c", stub, shazamScript, tt.fixture)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]string
			if err := json.Unmarshal(output, &result); err != nil || result["status"] != tt.status {
				t.Fatalf("helper result=%s err=%v", output, err)
			}
			if strings.Contains(string(output), "secret") || strings.Contains(string(output), "recording.wav") {
				t.Fatalf("helper exposed raw provider data: %s", output)
			}
			if tt.status == "ok" && (result["title"] != "Песня" || result["artist"] != "Исполнитель") {
				t.Fatalf("helper lost recognition metadata: %s", output)
			}
		})
	}
}

func TestShazamHelperRejectsUnsupportedPythonBeforeImport(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stub := "import asyncio, sys; sys.version_info = (3, 14, 0); exec(sys.argv[1], {'__name__': '__main__'})"
	output, err := runRecognitionCommand(ctx, python, "-c", stub, shazamScript)
	if err != nil || strings.TrimSpace(string(output)) != `{"status": "unavailable"}` {
		t.Fatalf("unsupported Python did not produce an unavailable result: %s err=%v", output, err)
	}
}

func TestShazamRecognizerDecodesAndBoundsOggVoice(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "voice.oga")
	_, err = runRecognitionCommand(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=61", "-c:a", "libopus", "-f", "ogg", path)
	if err != nil {
		t.Fatalf("prepare local Opus sample: %v", err)
	}
	// Verify the actual decoded WAV at the Python boundary without contacting Shazam.
	bin := filepath.Join(t.TempDir(), "python")
	script := "#!" + python + `
import json, sys, wave
with wave.open(sys.argv[-1], "rb") as audio:
    assert audio.getnchannels() == 1
    assert audio.getframerate() == 16000
    assert audio.getsampwidth() == 2
    assert audio.getnframes() == 60 * 16000
print(json.dumps({"status": "ok", "title": "Track", "artist": "Artist"}))
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &shazamRecognizer{python: bin, ffmpeg: ffmpeg}
	got, err := r.recognize(ctx, path)
	if err != nil || got.Title != "Track" || got.Artist != "Artist" {
		t.Fatalf("real voice decoding failed: track=%#v err=%v", got, err)
	}
}
