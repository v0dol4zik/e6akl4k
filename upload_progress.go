package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type statusReporter struct {
	app       *app
	message   *tgbotapi.Message
	lang      string
	cancelKey string
	updates   chan string
	cancel    context.CancelFunc
	mu        sync.Mutex
	lastAt    time.Time
	lastPct   int
}

type statusReporterContextKey struct{}

func withStatusReporter(ctx context.Context, reporter *statusReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, statusReporterContextKey{}, reporter)
}

func statusReporterFromContext(ctx context.Context) *statusReporter {
	reporter, _ := ctx.Value(statusReporterContextKey{}).(*statusReporter)
	return reporter
}

func newStatusReporter(ctx context.Context, application *app, message *tgbotapi.Message, lang, cancelKey string) *statusReporter {
	if application == nil || message == nil {
		return nil
	}
	reporterCtx, cancel := context.WithCancel(ctx)
	reporter := &statusReporter{app: application, message: message, lang: lang, cancelKey: cancelKey, updates: make(chan string, 1), cancel: cancel, lastPct: -1}
	go reporter.loop(reporterCtx)
	return reporter
}

func (r *statusReporter) loop(ctx context.Context) {
	for {
		select {
		case text := <-r.updates:
			edit := tgbotapi.NewEditMessageTextAndMarkup(r.message.Chat.ID, r.message.MessageID, text, *downloadCancelKeyboard(r.cancelKey, r.lang))
			edit.ParseMode = "HTML"
			_, _ = sendTelegram(r.app.bot, edit)
		case <-ctx.Done():
			return
		}
	}
}

func (r *statusReporter) close() {
	if r != nil {
		r.cancel()
	}
}

func (r *statusReporter) stage(text string) {
	if r == nil {
		return
	}
	select {
	case r.updates <- text:
	default:
		select {
		case <-r.updates:
		default:
		}
		select {
		case r.updates <- text:
		default:
		}
	}
}

func (r *statusReporter) upload(done, total int64) {
	if r == nil || total <= 0 {
		return
	}
	if done > total {
		done = total
	}
	percent := int(done * 100 / total)
	now := time.Now()
	r.mu.Lock()
	if percent < 100 && percent-r.lastPct < 5 && now.Sub(r.lastAt) < 2*time.Second {
		r.mu.Unlock()
		return
	}
	r.lastPct, r.lastAt = percent, now
	r.mu.Unlock()
	r.stage(tr("upload_progress", r.lang, "percent", strconv.Itoa(percent), "done", humanSize(done, r.lang), "total", humanSize(total, r.lang)))
}

type uploadBatchProgress struct {
	mu       sync.Mutex
	reporter *statusReporter
	total    int64
	files    map[string]int64
}

func newUploadBatchProgress(reporter *statusReporter, total int64) *uploadBatchProgress {
	return &uploadBatchProgress{reporter: reporter, total: total, files: make(map[string]int64)}
}

func (p *uploadBatchProgress) update(path string, read int64) {
	if p == nil || p.reporter == nil {
		return
	}
	p.mu.Lock()
	if read > p.files[path] {
		p.files[path] = read
	}
	var done int64
	for _, size := range p.files {
		done += size
	}
	p.mu.Unlock()
	p.reporter.upload(done, p.total)
}

type progressFile struct {
	path     string
	progress *uploadBatchProgress
}

func (file progressFile) NeedsUpload() bool { return true }
func (file progressFile) SendData() string  { panic("progressFile must be uploaded") }
func (file progressFile) UploadData() (string, io.Reader, error) {
	handle, err := os.Open(file.path)
	if err != nil {
		return "", nil, err
	}
	return filepath.Base(file.path), &progressReader{file: handle, path: file.path, progress: file.progress}, nil
}

type progressReader struct {
	file     *os.File
	path     string
	read     int64
	progress *uploadBatchProgress
}

func (reader *progressReader) Read(buffer []byte) (int, error) {
	count, err := reader.file.Read(buffer)
	reader.read += int64(count)
	if reader.progress != nil {
		reader.progress.update(reader.path, reader.read)
	}
	if err != nil {
		_ = reader.file.Close()
	}
	return count, err
}
