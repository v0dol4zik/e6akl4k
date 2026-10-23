package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

type sourceFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	refs     int
	finished bool
	results  []downloadResult
	err      error
}

type sourceFlightGroup struct {
	mu    sync.Mutex
	calls map[string]*sourceFlight
}

// prepareSource shares one extraction/transfer between concurrent recipients.
// Each recipient gets hard links in its own private session, so sending or
// cancelling one request cannot delete a file another request is still using.
func (a *app) prepareSource(ctx context.Context, item pendingURL, format, quality string, fn func(context.Context) ([]downloadResult, error)) ([]downloadResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if singleYouTubeID(item.URL) == "" || item.Preview.IsPlaylist {
		return fn(ctx)
	}
	g := &a.sourceFlights
	key := downloadIdentity(item.URL, format, quality)
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*sourceFlight)
	}
	call := g.calls[key]
	if call == nil {
		workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		stopShutdown := context.AfterFunc(a.ctx, cancel)
		call = &sourceFlight{done: make(chan struct{}), cancel: cancel}
		g.calls[key] = call
		go func() {
			results, err := fn(workCtx)
			stopShutdown()
			cancel()
			g.mu.Lock()
			call.results, call.err, call.finished = results, err, true
			unused := call.refs == 0
			// Signal only after cleanup on the last-recipient cancellation.
			if unused {
				a.downloader.clearSessions(results)
			}
			close(call.done)
			g.mu.Unlock()
		}()
	}
	call.refs++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		call.refs--
		last := call.refs == 0
		finished := call.finished
		if last {
			if g.calls[key] == call {
				delete(g.calls, key)
			}
			call.cancel()
		}
		g.mu.Unlock()
		if last {
			if finished {
				a.downloader.clearSessions(call.results)
			} else {
				// Cancellation returns only after the process group has stopped.
				<-call.done
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-call.done:
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if call.err != nil {
		return nil, call.err
	}
	return a.downloader.cloneResults(call.results)
}

func (d *downloader) cloneResults(original []downloadResult) ([]downloadResult, error) {
	results := append([]downloadResult(nil), original...)
	sessions := make(map[string]string)
	for i := range results {
		results[i].Session = ""
		if results[i].FilePath == "" {
			continue
		}
		source := filepath.Dir(original[i].FilePath)
		session := sessions[source]
		if session == "" {
			var err error
			session, err = randomID()
			if err == nil {
				err = os.Mkdir(filepath.Join(d.downloadDir, session), 0700)
			}
			if err != nil {
				d.clearSessions(results[:i])
				return nil, err
			}
			sessions[source] = session
		}
		results[i].Session = session
		path := filepath.Join(d.downloadDir, session, filepath.Base(original[i].FilePath))
		if err := os.Link(original[i].FilePath, path); err != nil {
			d.clearSessions(results[:i+1])
			return nil, err
		}
		results[i].FilePath = path
	}
	return results, nil
}
