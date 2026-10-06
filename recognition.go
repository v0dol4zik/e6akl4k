package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const maxVoiceBytes = 2 << 20

var errVoiceTooLarge = errors.New("voice sample exceeds 2 MiB")

func newVoiceFileClient() *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (a *app) handleVoiceRecognition(message *tgbotapi.Message) {
	chatID, userID := message.Chat.ID, message.From.ID
	lang, ok := a.getLang(userID)
	if !ok {
		a.sendText(chatID, chooseLanguageText, "", languageKeyboard())
		return
	}
	if allowed, retry := a.limiter.allow(userID); !allowed {
		if a.store != nil {
			a.store.increment(a.ctx, "rate_limited")
		}
		a.sendText(chatID, tr("rate_limited", lang, "seconds", strconv.Itoa(int(retry.Seconds())+1)), "", nil)
		return
	}
	switch {
	case message.Voice.Duration < 3:
		a.sendText(chatID, tr("recognition_too_short", lang), "", nil)
		return
	case message.Voice.Duration > 60:
		a.sendText(chatID, tr("recognition_too_long", lang), "", nil)
		return
	case message.Voice.FileSize > maxVoiceBytes:
		a.sendText(chatID, tr("recognition_too_large", lang), "", nil)
		return
	}
	if !a.beginUserDownload(userID) {
		a.sendText(chatID, tr("user_download_active", lang), "", nil)
		return
	}
	defer a.finishUserDownload(userID)
	status := a.sendText(chatID, tr("recognition_listening", lang), "HTML", nil)
	ctx, cancel := context.WithTimeout(a.ctx, 90*time.Second)
	defer cancel()
	track, err := a.recognizeVoice(ctx, message.Voice.FileID)
	if err != nil {
		a.voiceRecognitionError(chatID, userID, lang, status, err)
		return
	}
	label := html.EscapeString(track.Title)
	if track.Artist != "" {
		label = html.EscapeString(track.Artist) + " — " + label
	}
	caption := tr("recognition_found", lang, "track", label)
	a.replaceStatusText(chatID, status, caption+"\n"+tr("searching", lang))
	query := musicSearchQuery(mediaPreview{Title: track.Title, Artist: track.Artist})
	lookupCtx, cancelLookup := context.WithTimeout(ctx, 20*time.Second)
	// The voice duration describes the sample, not the full song, so it must not affect ranking.
	candidates, err := a.runRankedLookup(lookupCtx, query, 0)
	cancelLookup()
	if errors.Is(err, errNothingFound) {
		a.replaceStatusText(chatID, status, caption+"\n"+tr("recognition_no_source", lang))
		return
	}
	if err != nil {
		a.voiceRecognitionError(chatID, userID, lang, status, err)
		return
	}
	if a.store != nil {
		a.store.increment(a.ctx, "searches")
	}
	if len(candidates) > 0 && candidates[0].Match == "exact" && candidates[0].Confidence >= 85 {
		key, err := a.storeRecognizedCandidate(chatID, userID, candidates[0])
		if err != nil {
			a.voiceRecognitionError(chatID, userID, lang, status, err)
			return
		}
		a.replaceStatusText(chatID, status, caption)
		a.downloadForActiveUser(userID, chatID, key, "mp3", "320", lang, nil)
		return
	}
	keys := make([]string, 0, len(candidates))
	choices := make([]inlineCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		key, err := a.storeRecognizedCandidate(chatID, userID, candidate)
		if err != nil {
			a.voiceRecognitionError(chatID, userID, lang, status, err)
			return
		}
		keys, choices = append(keys, key), append(choices, candidate)
	}
	if len(keys) == 0 {
		a.replaceStatusText(chatID, status, caption+"\n"+tr("recognition_no_source", lang))
		return
	}
	markup := searchKeyboard(keys, choices, lang)
	for i, key := range keys {
		data := "dl:mp3:320:" + key
		markup.InlineKeyboard[i][0].CallbackData = &data
	}
	text := caption + "\n" + tr("recognition_choose", lang)
	if status != nil {
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, status.MessageID, text, *markup)
		edit.ParseMode = "HTML"
		if _, err := sendTelegram(a.bot, edit); err == nil {
			return
		}
	}
	a.sendText(chatID, text, "HTML", markup)
}

func (a *app) storeRecognizedCandidate(chatID, userID int64, candidate inlineCandidate) (string, error) {
	preview := mediaPreview{URL: candidate.URL, Title: candidate.Title, Artist: candidate.Artist,
		Duration: candidate.Duration, DurationSeconds: inlineDurationSeconds(candidate.Duration),
		TrackCount: 1, SourceID: candidate.SourceID, Extractor: candidate.Extractor}
	return a.storeURL(pendingURL{URL: candidate.URL, ChatID: chatID, UserID: userID, Preview: preview})
}

func (a *app) voiceRecognitionError(chatID, userID int64, lang string, status *tgbotapi.Message, err error) {
	if a.ctx.Err() != nil {
		return
	}
	key := "recognition_error"
	switch {
	case errors.Is(err, errMusicNotRecognized):
		key = "recognition_not_found"
	case errors.Is(err, errVoiceTooLarge):
		key = "recognition_too_large"
	case errors.Is(err, errInvalidVoiceAudio):
		key = "recognition_invalid_audio"
	case errors.Is(err, context.DeadlineExceeded):
		key = "recognition_timeout"
	case errors.Is(err, errQueueFull):
		key = "queue_full"
	default:
		if errors.Is(err, errRecognitionUnavailable) {
			key = "recognition_unavailable"
		}
		a.reportError(errorReport{Stage: "recognition", ChatID: chatID, UserID: userID, Error: err.Error()})
	}
	a.replaceStatusText(chatID, status, tr(key, lang))
}

func (a *app) recognizeVoice(ctx context.Context, fileID string) (recognizedTrack, error) {
	_, release, err := a.lookups.acquire(ctx)
	if err != nil {
		if errors.Is(err, errQueueFull) && a.store != nil {
			a.store.increment(a.ctx, "queue_rejected")
		}
		return recognizedTrack{}, err
	}
	defer release()
	session, err := os.MkdirTemp("", "musicbot-recognition-")
	if err != nil {
		return recognizedTrack{}, errors.New("create recognition temporary directory failed")
	}
	defer os.RemoveAll(session)
	path := filepath.Join(session, "voice")
	if err := a.downloadVoiceFile(ctx, fileID, path); err != nil {
		return recognizedTrack{}, err
	}
	return a.recognizer.recognize(ctx, path)
}

type voiceMetadataClient struct {
	ctx  context.Context
	base tgbotapi.HTTPClient
}

func (c voiceMetadataClient) Do(request *http.Request) (*http.Response, error) {
	return c.base.Do(request.WithContext(c.ctx))
}

func (a *app) downloadVoiceFile(ctx context.Context, fileID, path string) error {
	// A client copy gives getFile this job's context without changing the shared bot client.
	bot := *a.bot
	bot.Client = voiceMetadataClient{ctx: ctx, base: bot.Client}
	file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Telegram getFile failed")
	}
	if file.FileSize > maxVoiceBytes {
		return errVoiceTooLarge
	}
	address, err := voiceFileURL(a.cfg.TelegramAPIURL, a.bot.Token, file.FilePath)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return errors.New("create Telegram voice download request failed")
	}
	response, err := a.voiceFiles.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Telegram voice download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram voice download: HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxVoiceBytes {
		return errVoiceTooLarge
	}
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("create voice sample file failed")
	}
	n, copyErr := io.Copy(output, io.LimitReader(response.Body, maxVoiceBytes+1))
	closeErr := output.Close()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case n > maxVoiceBytes:
		return errVoiceTooLarge
	case copyErr != nil || closeErr != nil:
		return errors.New("save Telegram voice sample failed")
	case n == 0:
		return errInvalidVoiceAudio
	}
	return nil
}

// The endpoint stays on the configured Telegram server, including for absolute local getFile
// paths. Such paths are downloaded through HTTP and never opened as local files.
func voiceFileURL(apiURL, token, filePath string) (string, error) {
	if filePath == "" || strings.Contains(filePath, "://") || strings.ContainsAny(filePath, "\\\x00\r\n?#") || (apiURL == "" && strings.HasPrefix(filePath, "/")) {
		return "", errors.New("invalid Telegram voice file path")
	}
	parts := strings.Split(filePath, "/")
	for i, part := range parts {
		if part == "." || part == ".." {
			return "", errors.New("invalid Telegram voice file path")
		}
		parts[i] = url.PathEscape(part)
	}
	return firstNonEmpty(apiURL, "https://api.telegram.org") + "/file/bot" + token + "/" + strings.Join(parts, "/"), nil
}
