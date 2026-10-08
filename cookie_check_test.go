package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const authenticatedWatchLater = `{"_type":"playlist","id":"WL","availability":"private","extractor_key":"YoutubeTab","entries":[]}`

func TestClassifyCookieLogin(t *testing.T) {
	exit := errors.New("exit status 1")
	tests := []struct {
		name, stdout, stderr string
		err                  error
		want                 cookieLogin
	}{
		{"authenticated empty playlist", authenticatedWatchLater, "", nil, cookieLoginValid},
		{"empty zero exit", "", "", nil, cookieLoginUnknown},
		{"wrong playlist", strings.ReplaceAll(authenticatedWatchLater, `"WL"`, `"PLpublic"`), "", nil, cookieLoginUnknown},
		{"public response", strings.ReplaceAll(authenticatedWatchLater, `"private"`, `"public"`), "", nil, cookieLoginUnknown},
		{"anonymous", "", "The playlist does not exist", exit, cookieLoginInvalid},
		{"rotated warning on success", authenticatedWatchLater, "cookies are no longer valid", nil, cookieLoginInvalid},
		{"sign in warning on success", authenticatedWatchLater, "Sign in to confirm you're not a bot", nil, cookieLoginInvalid},
		{"network", "", "Unable to download API page", exit, cookieLoginUnknown},
		{"rate limited", "", "HTTP Error 429: Too Many Requests", exit, cookieLoginUnknown},
		{"timeout with login error", "", "Sign in", context.DeadlineExceeded, cookieLoginUnknown},
		{"canceled", "", "cookies are no longer valid", context.Canceled, cookieLoginUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _ := classifyCookieLogin([]byte(test.stdout), test.stderr, test.err)
			if got != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}

func TestClassifyCookieMedia(t *testing.T) {
	exit := errors.New("exit status 1")
	for _, test := range []struct {
		stderr string
		err    error
		want   cookieLogin
	}{
		{"", nil, cookieLoginValid},
		{"HTTP Error 403: Forbidden", exit, cookieLoginDegraded},
		{"Sign in to confirm you're not a bot", exit, cookieLoginDegraded},
		{"Requested format is not available", exit, cookieLoginDegraded},
		{"cookies are no longer valid", exit, cookieLoginInvalid},
		{"HTTP Error 429: Too Many Requests", exit, cookieLoginUnknown},
		{"PO token provider failed to generate tokens: HTTP Error 403", exit, cookieLoginUnknown},
		{"HTTP Error 403", context.DeadlineExceeded, cookieLoginUnknown},
	} {
		got, detail := classifyCookieMedia(test.stderr, test.err)
		if got != test.want {
			t.Errorf("%q: got %s (%s), want %s", test.stderr, got, detail, test.want)
		}
	}
}

// This process fake emulates login success followed by media failure, missing/partial output,
// and success. It records arguments and mutates only the isolated cookie copy.
func TestCheckCookiesRequiresCompleteSignedInAudio(t *testing.T) {
	for _, test := range []struct {
		mode  string
		want  cookieLogin
		calls int
	}{
		{"ok", cookieLoginValid, 2},
		{"login-invalid", cookieLoginInvalid, 1},
		{"login-empty", cookieLoginUnknown, 1},
		{"media-forbidden", cookieLoginDegraded, 2},
		{"media-rate-limit", cookieLoginUnknown, 2},
		{"media-skipped", cookieLoginUnknown, 2},
		{"media-partial", cookieLoginUnknown, 2},
	} {
		t.Run(test.mode, func(t *testing.T) {
			root := t.TempDir()
			work := filepath.Join(root, "work")
			if err := os.Mkdir(work, 0o700); err != nil {
				t.Fatal(err)
			}
			cookies := filepath.Join(root, "cookies.txt")
			original := []byte(test.mode + "\n")
			if err := os.WriteFile(cookies, original, 0o600); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "yt-dlp")
			script := `#!/bin/sh
root=$(dirname "$0")
printf '%s\n' "$@" >> "$root/args"
printf 'call\n' >> "$root/calls"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --cookies) cookies=$2; shift;;
    --output) output=$2; shift;;
    :ytwatchlater) login=yes;;
  esac
  shift
done
[ -n "$cookies" ] || exit 80
[ "$cookies" != "$root/cookies.txt" ] || exit 81
cp "$cookies" "$root/cookies-seen"
printf '# mutated\n' >> "$cookies"
if [ "$login" = yes ]; then
  if grep -q login-invalid "$cookies"; then printf 'ERROR: playlist does not exist\n' >&2; exit 1; fi
  if grep -q login-empty "$cookies"; then exit 0; fi
  printf '%s\n' '` + authenticatedWatchLater + `'
  exit 0
fi
if grep -q media-forbidden "$cookies"; then printf 'ERROR: HTTP Error 403: Forbidden\n' >&2; exit 1; fi
if grep -q media-rate-limit "$cookies"; then printf 'ERROR: HTTP Error 429: Too Many Requests\n' >&2; exit 1; fi
if grep -q media-skipped "$cookies"; then exit 0; fi
target=${output%/*}/audio.m4a
if grep -q media-partial "$cookies"; then target=$target.part; fi
printf 'audio\n' > "$target"
printf '{"id":"dQw4w9WgXcQ","path":"%s"}\n' "$target"
`
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			dl := &downloader{bin: bin, downloadDir: work, cookiesFile: cookies, youtubeCookieClients: "mweb", pluginDir: "/test-plugins", potProviderURL: "http://provider:4416"}
			result := dl.checkCookieLogin(context.Background())
			if result.Login != test.want || result.Fingerprint == "" {
				t.Fatalf("result=%#v", result)
			}
			args, _ := os.ReadFile(filepath.Join(root, "args"))
			calls, _ := os.ReadFile(filepath.Join(root, "calls"))
			if strings.Count(string(calls), "call\n") != test.calls {
				t.Fatalf("unexpected fallback: %s", calls)
			}
			if test.calls == 2 && (!strings.Contains(string(args), "youtube:player_client=mweb") || !strings.Contains(string(args), "youtubepot-bgutilhttp:base_url=http://provider:4416") || !strings.Contains(string(args), "--no-cache-dir") || strings.Contains(string(args), "--test\n")) {
				t.Fatalf("media args=%s", args)
			}
			if after, _ := os.ReadFile(cookies); string(after) != string(original) {
				t.Fatal("source cookies were modified")
			}
			if entries, _ := os.ReadDir(work); len(entries) != 0 {
				t.Fatalf("temporary files leaked: %v", entries)
			}
		})
	}
}

func TestCookieStatusRejectsLegacyChangedAndExpiredResults(t *testing.T) {
	application, state, _, now := newStatusTestApp(t, &statusTestTelegram{})
	ctx := context.Background()
	application.cfg.CookieCheckInterval = time.Hour
	if status := application.cookieStatusComponent(ctx, now.Add(2*time.Hour), "en"); status.Level != statusWarn || !strings.Contains(status.Detail, "expired") {
		t.Fatalf("expired=%#v", status)
	}
	if err := os.WriteFile(application.downloader.cookiesFile, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := application.cookieStatusComponent(ctx, *now, "en"); status.Level != statusWarn || !strings.Contains(status.Detail, "cookies changed") {
		t.Fatalf("changed=%#v", status)
	}
	_ = state.setMetadata(ctx, cookieCheckStateKey, "")
	_ = state.setMetadata(ctx, "cookie_login_result", "ok")
	_ = state.setMetadata(ctx, "cookie_login_at", "9999999999")
	if status := application.cookieStatusComponent(ctx, *now, "en"); status.Level != statusWarn || !strings.Contains(status.Detail, "not checked yet") {
		t.Fatalf("legacy=%#v", status)
	}
	_ = os.Remove(application.downloader.cookiesFile)
	if status := application.cookieStatusComponent(ctx, *now, "en"); status.Level != statusWarn || !strings.Contains(status.Detail, "cannot be read") {
		t.Fatalf("unreadable=%#v", status)
	}
}

func TestCookieCheckKeepsIdentityWhenReplacedDuringCheck(t *testing.T) {
	application, _, _, _ := newStatusTestApp(t, &statusTestTelegram{})
	ctx := context.Background()
	before := cookieTestResult(t, application, cookieLoginValid, "full download")
	application.cookieLoginCheck = func(context.Context) cookieCheckResult {
		if err := os.WriteFile(application.downloader.cookiesFile, []byte("new cookies"), 0o600); err != nil {
			t.Fatal(err)
		}
		return before
	}
	if !application.cookieAlerts.startLoginCheck(ctx, application.store, before.Fingerprint, true) {
		t.Fatal("check not started")
	}
	application.executeCookieCheck(ctx)
	application.cookieAlerts.checks.Done()
	if got := application.cookieStatusComponent(ctx, time.Now(), "en"); got.Level != statusWarn || !strings.Contains(got.Detail, "cookies changed") {
		t.Fatalf("old check accepted: %#v", got)
	}
	var persisted cookieCheckResult
	if err := json.Unmarshal([]byte(application.store.metadata(ctx, cookieCheckStateKey)), &persisted); err != nil || persisted.Fingerprint != before.Fingerprint {
		t.Fatalf("check identity changed: %#v %v", persisted, err)
	}
}

func TestCookieCheckTriggersShareSingleGate(t *testing.T) {
	application, _, _, _ := newStatusTestApp(t, &statusTestTelegram{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	application.cfg.CookieCheckInterval = time.Millisecond
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	application.cookieLoginCheck = func(context.Context) cookieCheckResult {
		calls.Add(1)
		close(started)
		<-release
		return cookieTestResult(t, application, cookieLoginUnknown, "test")
	}
	_ = application.store.setMetadata(ctx, cookieCheckStateKey, "")
	done := make(chan struct{})
	go func() { application.runCookieChecks(ctx); close(done) }()
	<-started
	application.suspectStaleCookies("admin_cookie_warning")
	if application.cookieAlerts.startLoginCheck(ctx, application.store, "", true) {
		t.Fatal("manual check overlapped the schedule")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	cancel()
	close(release)
	<-done
}

func TestCookieTrustClearsAfterFailureOrReplacement(t *testing.T) {
	ctx := context.Background()
	var state cookieAlertState
	state.finishLoginCheck(cookieLoginValid, "old")
	if state.startLoginCheck(ctx, nil, "old", false) {
		t.Fatal("fresh success should be trusted")
	}
	state.finishLoginCheck(cookieLoginUnknown, "old")
	if !state.startLoginCheck(ctx, nil, "old", false) {
		t.Fatal("unknown result retained successful trust")
	}
	state.finishLoginCheck(cookieLoginValid, "old")
	state.checks.Done()
	if !state.startLoginCheck(ctx, nil, "new", false) {
		t.Fatal("replacement retained successful trust")
	}
	state.finishLoginCheck(cookieLoginUnknown, "new")
	state.checks.Done()
}

func TestCookieCheckTimeoutCleansUp(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	cookies := filepath.Join(root, "cookies.txt")
	if err := os.WriteFile(cookies, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "yt-dlp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := (&downloader{bin: bin, cookiesFile: cookies, downloadDir: work}).checkCookieLogin(ctx)
	if result.Login != cookieLoginUnknown || result.Detail != "check timed out" {
		t.Fatalf("timeout=%#v", result)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatalf("timeout leaked temporary files: %v", entries)
	}
}

func TestFailedMediaCheckReplacesGreenStatus(t *testing.T) {
	application, _, _, _ := newStatusTestApp(t, &statusTestTelegram{})
	ctx := context.Background()
	application.cookieLoginCheck = func(context.Context) cookieCheckResult {
		return cookieTestResult(t, application, cookieLoginDegraded, "signed-in media returned 403")
	}
	if !application.cookieAlerts.startLoginCheck(ctx, application.store, "", true) {
		t.Fatal("check not started")
	}
	application.executeCookieCheck(ctx)
	application.cookieAlerts.checks.Done()
	if got := application.cookieStatusComponent(ctx, time.Now(), "en"); got.Level != statusWarn || !strings.Contains(got.Detail, "download with cookies failed") {
		t.Fatalf("media failure remained green: %#v", got)
	}
	if got := application.cookieStatus(ctx); got == "ok" {
		t.Fatal("health endpoint retained false success")
	}
}
