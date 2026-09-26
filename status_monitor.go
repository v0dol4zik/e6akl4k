package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	statusMonitorInterval = time.Minute
	// statusRefreshInterval re-renders the pinned message even when nothing changed, so its
	// "updated" time shows that the monitor itself is alive.
	statusRefreshInterval = 10 * time.Minute
	// statusMessageKey stores "<chat>:<message>" of the pinned status message.
	statusMessageKey = "status_message"
	// statusComponentsKey stores the last level of every component, so transitions survive restarts.
	statusComponentsKey = "status_components"
	// cookieLogin*Key persist the last login check with the configured cookies.
	cookieLoginResultKey = "cookie_login_result"
	cookieLoginAtKey     = "cookie_login_at"
	cookieLoginDetailKey = "cookie_login_detail"
	// ytdlpLatest*Key cache the newest yt-dlp release, asked for once per ytdlpReleaseCheckInterval.
	ytdlpLatestKey            = "ytdlp_latest"
	ytdlpLatestAtKey          = "ytdlp_latest_at"
	ytdlpReleaseCheckInterval = 6 * time.Hour
	ytdlpReleaseURL           = "https://github.com/yt-dlp/yt-dlp/releases/latest"
	// statusDroppedWarning keeps the reports component yellow after a report was dropped.
	statusDroppedWarning = time.Hour
	// statusSpikeWarning keeps the header yellow after a spike of one error.
	statusSpikeWarning = time.Hour
)

type statusLevel int

const (
	statusOK statusLevel = iota
	statusWarn
	statusFail
)

func (l statusLevel) icon() string {
	switch l {
	case statusWarn:
		return "🟡"
	case statusFail:
		return "🔴"
	}
	return "🟢"
}

// statusComponent is one line of the status message. Label and Detail are HTML-safe.
type statusComponent struct {
	Key    string
	Label  string
	Level  statusLevel
	Detail string
}

type statusComponentState struct {
	Level statusLevel `json:"level"`
	Since int64       `json:"since"`
	// Alerted is false while the announcement of this level has not reached the chat yet.
	Alerted bool `json:"alerted"`
}

// statusMonitor keeps the pinned status message of the error chat up to date and announces
// component failures and recoveries there.
type statusMonitor struct {
	chatID    int64
	messageID int
	loaded    bool
	stable    string
	lastEdit  time.Time
	states    map[string]statusComponentState

	dropped          int64
	droppedSeen      bool
	droppedRecent    int64
	droppedWarnUntil time.Time
	pinWarned        bool

	now            func() time.Time
	dial           func(ctx context.Context, network, address string) (net.Conn, error)
	latestRelease  func(ctx context.Context) (string, error)
	installedYtdlp func() string
}

func newStatusMonitor(chatID int64) *statusMonitor {
	return &statusMonitor{
		chatID:        chatID,
		states:        make(map[string]statusComponentState),
		dial:          (&net.Dialer{}).DialContext,
		latestRelease: fetchLatestYtdlpRelease,
	}
}

func (m *statusMonitor) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// startStatusMonitor runs the status message and the periodic cookie login check. It needs the
// error chat, the database and STATUS_MESSAGE=true.
func (a *app) startStatusMonitor(ctx context.Context) {
	reporter := a.errorReports
	if reporter == nil || a.store == nil || !a.cfg.StatusMessage {
		return
	}
	monitor := newStatusMonitor(reporter.chatID)
	if a.downloader != nil {
		monitor.installedYtdlp = a.downloader.ytdlpVersion
	}
	go a.runCookieChecks(ctx)
	go func() {
		ticker := time.NewTicker(statusMonitorInterval)
		defer ticker.Stop()
		for {
			a.statusTick(ctx, monitor)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (a *app) statusTick(ctx context.Context, m *statusMonitor) {
	if !m.loaded {
		m.loaded = true
		a.loadStatusState(ctx, m)
	}
	now := m.clock()
	components := a.statusComponents(ctx, m, now)
	a.announceStatusChanges(ctx, m, components, now)
	text, stable := a.renderStatus(ctx, m, components, now, defaultLang)
	a.publishStatus(ctx, m, text, stable, now)
}

func (a *app) loadStatusState(ctx context.Context, m *statusMonitor) {
	if chat, message, ok := strings.Cut(a.store.metadata(ctx, statusMessageKey), ":"); ok {
		chatID, chatErr := strconv.ParseInt(chat, 10, 64)
		messageID, messageErr := strconv.Atoi(message)
		if chatErr == nil && messageErr == nil && chatID == m.chatID && messageID > 0 {
			m.messageID = messageID
		}
	}
	if raw := a.store.metadata(ctx, statusComponentsKey); raw != "" {
		states := make(map[string]statusComponentState)
		if err := json.Unmarshal([]byte(raw), &states); err == nil {
			m.states = states
		}
	}
}

// statusComponents checks every dependency the bot needs to serve users.
func (a *app) statusComponents(ctx context.Context, m *statusMonitor, now time.Time) []statusComponent {
	lang := defaultLang
	components := []statusComponent{a.telegramStatus(lang), a.databaseStatus(ctx, lang), a.diskStatus(lang), a.cookieStatusComponent(ctx, now, lang)}
	if a.cfg.YandexProxy != nil {
		components = append(components, a.relayStatus(ctx, m, lang))
	}
	components = append(components, a.ytdlpStatus(ctx, m, now, lang), a.reportsStatus(ctx, m, now, lang))
	return components
}

func (a *app) telegramStatus(lang string) statusComponent {
	component := statusComponent{Key: "telegram", Label: tr("status_telegram", lang), Detail: tr("status_works", lang)}
	if a.cfg.TelegramAPIURL != "" {
		component.Label = tr("status_telegram_local", lang)
	}
	if _, err := a.bot.GetMe(); err != nil {
		component.Level = statusFail
		component.Detail = tr("status_unreachable", lang, "reason", networkReason(err, lang))
	}
	return component
}

func (a *app) databaseStatus(ctx context.Context, lang string) statusComponent {
	component := statusComponent{Key: "database", Label: tr("status_database", lang), Detail: tr("status_works", lang)}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var one int
	if err := a.store.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		component.Level = statusFail
		component.Detail = tr("status_error", lang, "error", html.EscapeString(shortenRunes(err.Error(), 120)))
	}
	return component
}

func (a *app) diskStatus(lang string) statusComponent {
	component := statusComponent{Key: "disk", Label: tr("status_disk", lang)}
	var stat syscall.Statfs_t
	if a.downloader == nil {
		component.Level = statusWarn
		component.Detail = tr("status_unknown", lang)
		return component
	}
	if err := syscall.Statfs(filepath.Clean(a.downloader.downloadDir), &stat); err != nil {
		component.Level = statusFail
		component.Detail = tr("status_error", lang, "error", html.EscapeString(err.Error()))
		return component
	}
	free := int64(stat.Bavail * uint64(stat.Bsize))
	component.Detail = tr("status_disk_free", lang, "free", humanSize(free, lang))
	switch threshold := a.cfg.DiskWarningBytes; {
	case threshold > 0 && free < threshold/4:
		component.Level = statusFail
	case threshold > 0 && free < threshold:
		component.Level = statusWarn
	}
	return component
}

// cookieStatusComponent shows the last login check with the configured cookies and the age of
// the cookies file. Downloads go anonymous first, so dead cookies break only age-restricted and
// members-only videos, but they are the one thing an operator has to replace by hand.
func (a *app) cookieStatusComponent(ctx context.Context, now time.Time, lang string) statusComponent {
	component := statusComponent{Key: "cookies", Label: tr("status_cookies", lang)}
	if a.downloader == nil || a.downloader.cookiesFile == "" {
		component.Level = statusWarn
		component.Detail = tr("status_cookies_missing", lang)
		return component
	}
	checked := metadataTime(a.store.metadata(ctx, cookieLoginAtKey))
	detail := html.EscapeString(a.store.metadata(ctx, cookieLoginDetailKey))
	ago := ""
	if !checked.IsZero() {
		ago = statusDuration(now.Sub(checked), lang)
	}
	switch a.store.metadata(ctx, cookieLoginResultKey) {
	case cookieLoginValid.String():
		component.Detail = tr("status_cookies_ok", lang, "ago", ago)
	case cookieLoginInvalid.String():
		component.Level = statusFail
		component.Detail = tr("status_cookies_failed", lang, "ago", ago)
	case cookieLoginUnknown.String():
		component.Level = statusWarn
		component.Detail = tr("status_cookies_unknown", lang, "ago", ago)
	default:
		component.Detail = tr("status_cookies_pending", lang)
	}
	if detail != "" && component.Level != statusOK {
		component.Detail += " · <code>" + detail + "</code>"
	}
	if info, err := os.Stat(a.downloader.cookiesFile); err == nil {
		component.Detail += " · " + tr("status_cookies_file", lang, "ago", statusDuration(now.Sub(info.ModTime()), lang))
	}
	return component
}

func (a *app) relayStatus(ctx context.Context, m *statusMonitor, lang string) statusComponent {
	component := statusComponent{Key: "relay", Label: tr("status_relay", lang), Detail: tr("status_works", lang)}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := m.dial(ctx, "tcp", proxyDialAddress(a.cfg.YandexProxy))
	if err != nil {
		// The proxy address may carry credentials, so only the kind of failure is shown.
		component.Level = statusFail
		component.Detail = tr("status_unreachable", lang, "reason", networkReason(err, lang))
		return component
	}
	_ = conn.Close()
	return component
}

// proxyDialAddress is host:port of a proxy URL with the scheme's default port.
func proxyDialAddress(proxy *url.URL) string {
	port := proxy.Port()
	if port == "" {
		switch proxy.Scheme {
		case "socks5", "socks5h":
			port = "1080"
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(proxy.Hostname(), port)
}

func (a *app) ytdlpStatus(ctx context.Context, m *statusMonitor, now time.Time, lang string) statusComponent {
	component := statusComponent{Key: "ytdlp", Label: "yt-dlp"}
	current := ""
	if m.installedYtdlp != nil {
		current = m.installedYtdlp()
	}
	latest := a.latestYtdlpRelease(ctx, m, now)
	switch {
	case current == "":
		component.Level = statusWarn
		component.Detail = tr("status_unknown", lang)
	case latest != "" && ytdlpVersionNewer(latest, current):
		component.Level = statusWarn
		component.Detail = tr("status_ytdlp_update", lang, "current", html.EscapeString(current), "latest", html.EscapeString(latest))
	default:
		component.Detail = html.EscapeString(current)
	}
	return component
}

// latestYtdlpRelease returns the cached newest release and refreshes it once per interval. The
// attempt time is saved first, so an unreachable GitHub is asked at most once per interval.
func (a *app) latestYtdlpRelease(ctx context.Context, m *statusMonitor, now time.Time) string {
	latest := a.store.metadata(ctx, ytdlpLatestKey)
	if now.Sub(metadataTime(a.store.metadata(ctx, ytdlpLatestAtKey))) < ytdlpReleaseCheckInterval || m.latestRelease == nil {
		return latest
	}
	_ = a.store.setMetadata(ctx, ytdlpLatestAtKey, strconv.FormatInt(now.Unix(), 10))
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	version, err := m.latestRelease(ctx)
	if err != nil {
		log.Printf("Узнать последнюю версию yt-dlp: %v", err)
		return latest
	}
	_ = a.store.setMetadata(ctx, ytdlpLatestKey, version)
	return version
}

var ytdlpVersionPattern = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}(\.\d+)?$`)

// fetchLatestYtdlpRelease reads the tag GitHub redirects /releases/latest to, without the API
// and its rate limit.
func fetchLatestYtdlpRelease(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, ytdlpReleaseURL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	_ = response.Body.Close()
	location := response.Header.Get("Location")
	if response.StatusCode/100 != 3 || location == "" {
		return "", fmt.Errorf("unexpected answer %d", response.StatusCode)
	}
	version := path.Base(location)
	if !ytdlpVersionPattern.MatchString(version) {
		return "", fmt.Errorf("unexpected release tag %q", shortenRunes(version, 40))
	}
	return version, nil
}

// ytdlpVersionNewer compares dotted numeric versions such as 2026.08.19 and 2026.08.19.232812.
func ytdlpVersionNewer(latest, current string) bool {
	parse := func(version string) []int {
		var parts []int
		for _, field := range strings.Split(strings.TrimSpace(version), ".") {
			number, err := strconv.Atoi(field)
			if err != nil {
				return nil
			}
			parts = append(parts, number)
		}
		return parts
	}
	newer, installed := parse(latest), parse(current)
	if newer == nil || installed == nil {
		return false
	}
	for i := 0; i < max(len(newer), len(installed)); i++ {
		var x, y int
		if i < len(newer) {
			x = newer[i]
		}
		if i < len(installed) {
			y = installed[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// reportsStatus turns yellow for an hour after the report queue overflowed and dropped reports.
func (a *app) reportsStatus(ctx context.Context, m *statusMonitor, now time.Time, lang string) statusComponent {
	component := statusComponent{Key: "reports", Label: tr("status_reports", lang), Detail: tr("status_reports_ok", lang)}
	counters, err := a.store.counters(ctx)
	if err != nil {
		return component
	}
	dropped := counters["error_reports_dropped"]
	switch {
	case !m.droppedSeen:
		m.dropped, m.droppedSeen = dropped, true
	case dropped > m.dropped:
		m.droppedRecent += dropped - m.dropped
		m.dropped = dropped
		m.droppedWarnUntil = now.Add(statusDroppedWarning)
	}
	if now.Before(m.droppedWarnUntil) {
		component.Level = statusWarn
		component.Detail = tr("status_reports_dropped", lang, "count", strconv.FormatInt(m.droppedRecent, 10))
	} else {
		m.droppedRecent = 0
	}
	return component
}

// announceStatusChanges posts a loud alert when a component fails, a silent note when it turns
// yellow and a silent recovery note, which is loud when the failure itself never got through.
func (a *app) announceStatusChanges(ctx context.Context, m *statusMonitor, components []statusComponent, now time.Time) {
	changed := false
	for _, component := range components {
		previous, known := m.states[component.Key]
		if !known {
			previous = statusComponentState{Level: statusOK, Since: now.Unix(), Alerted: true}
		}
		next := previous
		switch {
		case component.Level != previous.Level:
			next = statusComponentState{Level: component.Level, Since: now.Unix()}
			next.Alerted = a.sendStatusAlert(m, component, previous, now)
		case component.Level == statusFail && !previous.Alerted:
			next.Alerted = a.sendStatusAlert(m, component, previous, now)
		}
		if next != previous || !known {
			changed = true
		}
		m.states[component.Key] = next
	}
	if changed {
		if raw, err := json.Marshal(m.states); err == nil {
			_ = a.store.setMetadata(ctx, statusComponentsKey, string(raw))
		}
	}
}

func (a *app) sendStatusAlert(m *statusMonitor, component statusComponent, previous statusComponentState, now time.Time) bool {
	lang := defaultLang
	var text string
	loud := false
	switch component.Level {
	case statusFail:
		text = tr("status_alert_fail", lang, "component", component.Label, "detail", component.Detail)
		loud = true
	case statusWarn:
		text = tr("status_alert_warn", lang, "component", component.Label, "detail", component.Detail)
	default:
		duration := statusDuration(now.Sub(time.Unix(previous.Since, 0)), lang)
		if previous.Level == statusFail && !previous.Alerted {
			text = tr("status_alert_recovered_unnoticed", lang, "component", component.Label, "duration", duration)
			loud = true
		} else {
			text = tr("status_alert_recovered", lang, "component", component.Label, "duration", duration)
		}
	}
	message := tgbotapi.NewMessage(m.chatID, text)
	message.ParseMode = "HTML"
	message.DisableWebPagePreview = true
	message.DisableNotification = !loud
	if _, err := sendTelegram(a.bot, message); err != nil {
		log.Printf("Не удалось отправить уведомление о статусе %s: %v", component.Key, err)
		return false
	}
	return true
}

// renderStatus returns the message text and its stable part: the lines that change only when
// something happens, without the queue, uptime and update time.
func (a *app) renderStatus(ctx context.Context, m *statusMonitor, components []statusComponent, now time.Time, lang string) (string, string) {
	worst := statusOK
	for _, component := range components {
		worst = max(worst, component.Level)
	}
	var spike errorSpikeRecord
	if a.errorReports != nil {
		spike = a.errorReports.lastSpikeRecord()
	}
	if !spike.At.IsZero() && now.Sub(spike.At) < statusSpikeWarning {
		worst = max(worst, statusWarn)
	}
	summary := map[statusLevel]string{statusOK: "status_summary_ok", statusWarn: "status_summary_warn", statusFail: "status_summary_fail"}[worst]
	stable := []string{tr("status_title", lang, "icon", worst.icon(), "summary", tr(summary, lang)), ""}
	for _, component := range components {
		stable = append(stable, component.Level.icon()+" "+component.Label+": "+component.Detail)
	}
	stable = append(stable, "")
	real, expected, err := a.store.errorReportCounts(ctx, now.Add(-24*time.Hour))
	if err == nil {
		stable = append(stable, tr("status_errors_day", lang, "real", strconv.Itoa(real), "expected", strconv.Itoa(expected)))
	}
	if last, ok := a.store.lastRealErrorReport(ctx); ok {
		stable = append(stable, tr("status_errors_last", lang, "time", statusTime(last.CreatedAt, now), "stage", html.EscapeString(last.Stage), "id", html.EscapeString(last.ID)))
	}
	if !spike.At.IsZero() && now.Sub(spike.At) < 24*time.Hour {
		stable = append(stable, tr("status_errors_spike", lang, "time", statusTime(spike.At, now), "stage", html.EscapeString(spike.Stage), "users", strconv.Itoa(spike.Users), "id", html.EscapeString(spike.ID)))
	}
	volatile := []string{}
	if a.downloads != nil {
		active, waiting, capacity := a.downloads.snapshot()
		volatile = append(volatile, tr("status_queue", lang, "active", strconv.Itoa(active), "capacity", strconv.Itoa(capacity), "waiting", strconv.Itoa(waiting)))
	}
	volatile = append(volatile, "",
		tr("status_version", lang, "version", html.EscapeString(a.reportVersion()), "uptime", statusDuration(now.Sub(processStarted), lang)),
		tr("status_updated", lang, "time", now.UTC().Format("02.01 15:04")))
	stableText := strings.Join(stable, "\n")
	return stableText + "\n" + strings.Join(volatile, "\n"), stableText
}

// publishStatus edits the pinned status message when its stable part changed or the refresh
// interval passed, and posts and pins a new one when the old message is gone.
func (a *app) publishStatus(ctx context.Context, m *statusMonitor, text, stable string, now time.Time) {
	if m.messageID != 0 {
		if stable == m.stable && now.Sub(m.lastEdit) < statusRefreshInterval {
			return
		}
		edit := tgbotapi.NewEditMessageText(m.chatID, m.messageID, text)
		edit.ParseMode = "HTML"
		edit.DisableWebPagePreview = true
		_, err := requestTelegram(a.bot, edit)
		if err == nil || strings.Contains(err.Error(), "message is not modified") {
			m.stable, m.lastEdit = stable, now
			return
		}
		if !statusMessageGone(err) {
			log.Printf("Обновить сообщение статуса: %v", err)
			return
		}
		m.messageID = 0
	}
	message := tgbotapi.NewMessage(m.chatID, text)
	message.ParseMode = "HTML"
	message.DisableWebPagePreview = true
	message.DisableNotification = true
	sent, err := sendTelegram(a.bot, message)
	if err != nil {
		log.Printf("Отправить сообщение статуса в чат %d: %v", m.chatID, err)
		return
	}
	m.messageID, m.stable, m.lastEdit = sent.MessageID, stable, now
	_ = a.store.setMetadata(ctx, statusMessageKey, strconv.FormatInt(m.chatID, 10)+":"+strconv.Itoa(sent.MessageID))
	pin := tgbotapi.PinChatMessageConfig{ChatID: m.chatID, MessageID: sent.MessageID, DisableNotification: true}
	if _, err := requestTelegram(a.bot, pin); err != nil {
		log.Printf("Закрепить сообщение статуса в чате %d: %v", m.chatID, err)
		if !m.pinWarned {
			m.pinWarned = true
			for _, adminID := range a.administratorIDs(ctx) {
				lang := a.langOrDefault(adminID)
				a.sendText(adminID, tr("status_pin_failed", lang, "error", html.EscapeString(err.Error())), "HTML", nil)
			}
		}
	}
}

func statusMessageGone(err error) bool {
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "message to edit not found") || strings.Contains(low, "message can't be edited") || strings.Contains(low, "message_id_invalid")
}

// runCookieChecks checks the login with the configured cookies every COOKIE_CHECK_INTERVAL,
// counting from the last check of any kind, so the status message always has a fresh verdict.
func (a *app) runCookieChecks(ctx context.Context) {
	interval := a.cfg.CookieCheckInterval
	if a.cookieLoginCheck == nil || interval <= 0 || a.downloader == nil || a.downloader.cookiesFile == "" {
		return
	}
	ticker := time.NewTicker(min(interval, 10*time.Minute))
	defer ticker.Stop()
	for {
		if time.Since(metadataTime(a.store.metadata(ctx, cookieLoginAtKey))) >= interval {
			result, detail := a.cookieLoginCheck(ctx)
			if ctx.Err() != nil {
				return
			}
			a.cookieAlerts.noteLoginResult(result)
			a.recordCookieLogin(ctx, result, detail)
			log.Printf("Плановая проверка входа YouTube по cookies: %s (%s)", result, detail)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// recordCookieLogin persists a login check for the status message.
func (a *app) recordCookieLogin(ctx context.Context, result cookieLogin, detail string) {
	if a.store == nil {
		return
	}
	_ = a.store.setMetadata(ctx, cookieLoginResultKey, result.String())
	_ = a.store.setMetadata(ctx, cookieLoginAtKey, strconv.FormatInt(time.Now().Unix(), 10))
	_ = a.store.setMetadata(ctx, cookieLoginDetailKey, shortenRunes(strings.ToValidUTF8(redactTraceText(detail), ""), 200))
}

func metadataTime(value string) time.Time {
	unix, err := strconv.ParseInt(value, 10, 64)
	if err != nil || unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

// networkReason names the kind of a connection failure without the address, which may be private.
func networkReason(err error, lang string) string {
	if apiErr, ok := telegramAPIError(err); ok {
		return tr("status_reason_api", lang, "code", strconv.Itoa(apiErr.Code))
	}
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr):
		return tr("status_reason_dns", lang)
	case errors.Is(err, syscall.ECONNREFUSED):
		return tr("status_reason_refused", lang)
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		return tr("status_reason_timeout", lang)
	}
	return tr("status_reason_other", lang)
}

// statusDuration renders a positive duration as minutes, hours or days.
func statusDuration(d time.Duration, lang string) string {
	switch {
	case d < time.Hour:
		return tr("duration_minutes", lang, "minutes", strconv.Itoa(max(1, int(d.Minutes()))))
	case d < 48*time.Hour:
		return tr("duration_hours", lang, "hours", strconv.Itoa(int(d.Hours())), "minutes", strconv.Itoa(int(d.Minutes())%60))
	}
	return tr("duration_days", lang, "days", strconv.Itoa(int(d.Hours()/24)))
}

// statusTime shows a UTC time, with the date when it is not today.
func statusTime(t, now time.Time) string {
	t, now = t.UTC(), now.UTC()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("02.01 15:04")
}
