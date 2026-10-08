package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNewgroundsProof(t *testing.T) {
	challenge := newgroundsChallenge{Algo: "sha256", Bits: 8, Payload: "dGVzdC1wYXlsb2Fk", Sig: "fixture-signature"}
	// SHA-256("test-payload:583") is 00937026...; all smaller nonces fail eight bits.
	if nonce, err := solveNewgroundsChallenge(context.Background(), challenge); err != nil || nonce != "583" {
		t.Fatalf("nonce=%q err=%v", nonce, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := solveNewgroundsChallenge(ctx, challenge); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled proof: %v", err)
	}
	challenge.Algo, challenge.Bits = "argon2id", 0
	challenge.Params = &newgroundsArgon2Params{HashLength: 32, Iterations: 1, MemorySize: 32, Parallelism: 1}
	if nonce, err := solveNewgroundsChallenge(context.Background(), challenge); err != nil || nonce != "0" {
		t.Fatalf("argon2 nonce=%q err=%v", nonce, err)
	}
}

func TestNewgroundsProofRejectsUnboundedResources(t *testing.T) {
	valid := func() newgroundsChallenge {
		return newgroundsChallenge{Algo: "argon2id", Bits: 8, Payload: "dGVzdC1wYXlsb2Fk", Sig: "fixture-signature", Params: &newgroundsArgon2Params{32, 1, 4096, 1}}
	}
	for name, mutate := range map[string]func(*newgroundsChallenge){
		"unknown algorithm": func(c *newgroundsChallenge) { c.Algo = "remote-javascript" },
		"invalid payload":   func(c *newgroundsChallenge) { c.Payload = "%broken" },
		"large payload":     func(c *newgroundsChallenge) { c.Payload = strings.Repeat("YQ", 5000) },
		"missing signature": func(c *newgroundsChallenge) { c.Sig = "" },
		"large signature":   func(c *newgroundsChallenge) { c.Sig = strings.Repeat("x", 1025) },
		"negative bits":     func(c *newgroundsChallenge) { c.Bits = -1 },
		"sha difficulty":    func(c *newgroundsChallenge) { c.Algo, c.Bits = "sha256", 23 },
		"argon difficulty":  func(c *newgroundsChallenge) { c.Bits = 13 },
		"missing params":    func(c *newgroundsChallenge) { c.Params = nil },
		"memory limit":      func(c *newgroundsChallenge) { c.Params.MemorySize = 16385 },
		"small memory":      func(c *newgroundsChallenge) { c.Params.MemorySize = 7 },
		"iterations":        func(c *newgroundsChallenge) { c.Params.Iterations = 3 },
		"parallelism":       func(c *newgroundsChallenge) { c.Params.Parallelism = 3 },
		"hash size":         func(c *newgroundsChallenge) { c.Params.HashLength = 4096 },
	} {
		t.Run(name, func(t *testing.T) {
			challenge := valid()
			mutate(&challenge)
			if _, err := solveNewgroundsChallenge(context.Background(), challenge); err == nil {
				t.Fatal("unsafe challenge was accepted")
			}
		})
	}
}

type newgroundsRoundTripper struct{ handler http.Handler }

func (r newgroundsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	return (handlerClient{handler: r.handler}).Do(req)
}

func newgroundsFixtureClient(t *testing.T, paths *[]string, failure string) *http.Client {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.URL.Path)
		if r.URL.Scheme != "https" || r.URL.Host != "www.newgrounds.com" || r.Header.Get("User-Agent") != newgroundsUserAgent {
			t.Errorf("unsafe request: %s", r.URL)
		}
		if failure == "redirect" {
			http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
			return
		}
		switch r.URL.Path {
		case "/audio/listen/549479":
			w.WriteHeader(http.StatusForbidden)
			if failure == "oversized" {
				fmt.Fprint(w, strings.Repeat("x", newgroundsGuardBodyLimit+1))
			} else if failure == "ordinary 403" {
				fmt.Fprint(w, "Forbidden")
			} else {
				fmt.Fprint(w, "<title>NG Guard</title>")
			}
		case "/_guard/api/v1/challenge":
			if failure == "rate limited" {
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{}`)
				return
			}
			fmt.Fprint(w, `{"algo":"sha256","bits":8,"payload":"dGVzdC1wYXlsb2Fk","sig":"fixture-signature"}`)
		case "/_guard/api/v1/verify":
			var solution struct {
				newgroundsChallenge
				Nonce       string `json:"nonce"`
				SolveTimeMS int64  `json:"solveTimeMs"`
				Demo        bool   `json:"demo"`
			}
			if err := json.NewDecoder(r.Body).Decode(&solution); err != nil {
				t.Fatal(err)
			}
			if solution.Nonce != "583" || solution.Payload != "dGVzdC1wYXlsb2Fk" || solution.Sig != "fixture-signature" || solution.Demo || solution.SolveTimeMS < 1 || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("invalid solution: %+v", solution)
			}
			if failure == "verification" {
				fmt.Fprint(w, `{"ok":false}`)
				return
			}
			if failure != "missing cookies" {
				http.SetCookie(w, &http.Cookie{Name: "ng_guard", Value: "fixture-clearance", Path: "/", Secure: true, HttpOnly: true})
			}
			fmt.Fprint(w, `{"ok":true}`)
		default:
			t.Fatalf("unexpected guard path %q", r.URL.Path)
		}
	})
	return &http.Client{Transport: newgroundsRoundTripper{handler: handler}}
}

func TestNewgroundsGuardFailuresCleanUpAndNeverRedirect(t *testing.T) {
	for _, failure := range []string{"redirect", "oversized", "ordinary 403", "rate limited", "verification"} {
		t.Run(failure, func(t *testing.T) {
			var paths []string
			d := &downloader{downloadDir: t.TempDir(), newgroundsHTTPClient: newgroundsFixtureClient(t, &paths, failure)}
			_, cleanup, err := d.newgroundsGuardArgs(context.Background(), []string{"--", newgroundsOrigin + "/audio/listen/549479"}, "")
			cleanup()
			if err == nil {
				t.Fatal("failed guard was accepted")
			}
			if failure == "redirect" && len(paths) != 1 {
				t.Fatalf("followed unsafe redirect: %v", paths)
			}
			files, err := os.ReadDir(d.downloadDir)
			if err != nil || len(files) != 0 {
				t.Fatalf("temporary files leaked: %v err=%v", files, err)
			}
		})
	}
}

func TestNewgroundsGuardAcceptsIPClearance(t *testing.T) {
	var paths []string
	d := &downloader{downloadDir: t.TempDir(), newgroundsHTTPClient: newgroundsFixtureClient(t, &paths, "missing cookies")}
	_, cleanup, err := d.newgroundsGuardArgs(context.Background(), []string{"--", newgroundsOrigin + "/audio/listen/549479"}, "")
	cleanup()
	if err != nil || len(paths) != 3 {
		t.Fatalf("IP clearance: requests=%v err=%v", paths, err)
	}
}

func TestNewgroundsGuardRetriesWithIsolatedCookies(t *testing.T) {
	for _, progress := range []bool{false, true} {
		t.Run(fmt.Sprintf("progress=%v", progress), func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "fake-yt-dlp")
			script := `#!/bin/sh
cookies=''; agent=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --cookies) cookies="$2"; shift 2 ;;
    --user-agent) agent="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ -z "$cookies" ] || ! grep -q 'fixture-clearance' "$cookies"; then
  printf 'ERROR: [Newgrounds] 549479: HTTP Error 403: Forbidden\n' >&2
  exit 1
fi
[ -n "$agent" ] || exit 92
printf 'ok'
`
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(dir, "cookies.txt")
			original := []byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tfixture\toriginal\n")
			if err := os.WriteFile(base, original, 0o600); err != nil {
				t.Fatal(err)
			}
			var paths []string
			d := &downloader{bin: bin, downloadDir: dir, cookiesFile: base, cookieSnapshot: original, newgroundsHTTPClient: newgroundsFixtureClient(t, &paths, "")}
			args := []string{"--", newgroundsOrigin + "/audio/listen/549479"}
			var out []byte
			var err error
			if progress {
				out, _, err = d.runWithProgress(context.Background(), args, "", nil, 1)
			} else {
				out, _, err = d.run(context.Background(), args...)
			}
			if err != nil || string(out) != "ok" {
				t.Fatalf("guard retry: out=%q err=%v", out, err)
			}
			if !reflect.DeepEqual(paths, []string{"/audio/listen/549479", "/_guard/api/v1/challenge", "/_guard/api/v1/verify"}) {
				t.Fatalf("unexpected requests: %v", paths)
			}
			data, err := os.ReadFile(base)
			if err != nil || string(data) != string(original) {
				t.Fatalf("configured cookies changed: %q err=%v", data, err)
			}
			for _, pattern := range []string{".cookies-*.txt", ".newgrounds-extractor-*"} {
				leftovers, err := filepath.Glob(filepath.Join(dir, pattern))
				if err != nil || len(leftovers) != 0 {
					t.Fatalf("temporary files leaked: %v err=%v", leftovers, err)
				}
			}
		})
	}
}
