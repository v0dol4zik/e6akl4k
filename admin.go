package main

import (
	"context"
	"fmt"
	"html"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (a *app) isOwner(userID int64) bool { return a.cfg.AdminIDs[userID] }

func (a *app) isAdmin(userID int64) bool {
	if a.isOwner(userID) {
		return true
	}
	return a.store != nil && a.store.isDynamicAdmin(a.ctx, userID)
}

func (a *app) administratorIDs(ctx context.Context) []int64 {
	set := make(map[int64]bool, len(a.cfg.AdminIDs))
	for id := range a.cfg.AdminIDs {
		set[id] = true
	}
	if a.store != nil {
		if records, err := a.store.admins(ctx); err == nil {
			for _, record := range records {
				set[record.UserID] = true
			}
		}
	}
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func updateUser(update tgbotapi.Update) *tgbotapi.User {
	switch {
	case update.Message != nil:
		return update.Message.From
	case update.CallbackQuery != nil:
		return update.CallbackQuery.From
	case update.InlineQuery != nil:
		return update.InlineQuery.From
	case update.ChosenInlineResult != nil:
		return update.ChosenInlineResult.From
	default:
		return nil
	}
}

func (a *app) observeTelegramUsers(update tgbotapi.Update) {
	if a.store == nil {
		return
	}
	seen := make(map[int64]bool)
	add := func(user *tgbotapi.User) {
		if user == nil || user.ID <= 0 || seen[user.ID] {
			return
		}
		seen[user.ID] = true
		if err := a.store.observeTelegramUser(a.ctx, user); err != nil {
			log.Printf("Сохранить Telegram username пользователя %d: %v", user.ID, err)
		}
	}
	addMessage := func(message *tgbotapi.Message) {
		if message == nil {
			return
		}
		add(message.From)
		add(message.ForwardFrom)
		add(message.LeftChatMember)
		for index := range message.NewChatMembers {
			add(&message.NewChatMembers[index])
		}
		for _, entity := range message.Entities {
			add(entity.User)
		}
		for _, entity := range message.CaptionEntities {
			add(entity.User)
		}
		if message.ReplyToMessage != nil {
			add(message.ReplyToMessage.From)
			add(message.ReplyToMessage.ForwardFrom)
		}
	}
	add(updateUser(update))
	addMessage(update.Message)
	if update.CallbackQuery != nil {
		addMessage(update.CallbackQuery.Message)
	}
}

func (a *app) rejectBannedUpdate(update tgbotapi.Update) bool {
	user := updateUser(update)
	if user == nil || a.store == nil || a.isAdmin(user.ID) {
		return false
	}
	ban, banned := a.store.isBanned(a.ctx, user.ID)
	if !banned {
		return false
	}
	reason := strings.TrimSpace(ban.Reason)
	if reason == "" {
		reason = "—"
	}
	lang := a.langOrDefault(user.ID)
	switch {
	case update.Message != nil && update.Message.Chat != nil:
		a.sendText(update.Message.Chat.ID, tr("banned_notice", lang, "reason", html.EscapeString(reason)), "HTML", nil)
	case update.CallbackQuery != nil:
		config := tgbotapi.NewCallbackWithAlert(update.CallbackQuery.ID, tr("banned_notice", lang, "reason", reason))
		_, _ = requestTelegram(a.bot, config)
	case update.InlineQuery != nil:
		a.answerInline(update.InlineQuery.ID, nil, lang)
	}
	return true
}

func parseAdminTarget(arguments string) (int64, string, bool) {
	fields := strings.Fields(arguments)
	if len(fields) == 0 {
		return 0, "", false
	}
	id, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	return id, strings.TrimSpace(strings.TrimPrefix(arguments, fields[0])), true
}

func (a *app) requireAdmin(message *tgbotapi.Message, owner bool) bool {
	allowed := a.isAdmin(message.From.ID)
	if owner {
		allowed = a.isOwner(message.From.ID)
	}
	if !allowed {
		a.sendText(message.Chat.ID, tr("admin_forbidden", a.langOrDefault(message.From.ID)), "", nil)
	}
	return allowed
}

func (a *app) handleAdminCommand(message *tgbotapi.Message) bool {
	if message == nil || message.From == nil || message.Chat == nil {
		return false
	}
	command := strings.ToLower(message.Command())
	lang := a.langOrDefault(message.From.ID)
	switch command {
	case "id":
		fields := strings.Fields(message.CommandArguments())
		if len(fields) == 0 {
			a.sendText(message.Chat.ID, tr("admin_id_reply", lang, "id", strconv.FormatInt(message.From.ID, 10)), "HTML", nil)
			return true
		}
		username, valid := normalizeTelegramUsername(fields[0])
		if len(fields) != 1 || !valid {
			a.sendText(message.Chat.ID, tr("admin_usage", lang, "usage", "/id [@username]"), "HTML", nil)
			return true
		}
		if a.store == nil {
			a.sendText(message.Chat.ID, tr("id_lookup_unknown", lang, "username", html.EscapeString("@"+username)), "HTML", nil)
			return true
		}
		userID, found, err := a.store.telegramUserIDByUsername(a.ctx, username)
		if err != nil {
			log.Printf("Найти Telegram ID @%s: %v", username, err)
			a.sendText(message.Chat.ID, tr("id_lookup_error", lang), "", nil)
			return true
		}
		if !found {
			a.sendText(message.Chat.ID, tr("id_lookup_unknown", lang, "username", html.EscapeString("@"+username)), "HTML", nil)
			return true
		}
		a.sendText(message.Chat.ID, tr("id_lookup_reply", lang, "username", html.EscapeString("@"+username), "id", strconv.FormatInt(userID, 10)), "HTML", nil)
		return true
	case "stats":
		a.handleAdminStats(message)
		return true
	case "status", "jobs":
		a.handleAdminStatus(message)
		return true
	case "perf":
		a.handleAdminPerf(message)
		return true
	case "log":
		a.handleAdminDownloadLog(message)
		return true
	case "ban", "pardon", "unban", "addadmin", "deladmin", "admins", "banlist", "userinfo", "adminlog":
		// handled below
	default:
		return false
	}
	if a.store == nil {
		return true
	}
	ownerOnly := command == "addadmin" || command == "deladmin"
	if !a.requireAdmin(message, ownerOnly) {
		return true
	}

	switch command {
	case "ban":
		target, reason, ok := parseAdminTarget(message.CommandArguments())
		if !ok {
			a.sendText(message.Chat.ID, tr("admin_usage", lang, "usage", "/ban <tg_id> [причина]"), "HTML", nil)
			return true
		}
		if a.isAdmin(target) {
			a.sendText(message.Chat.ID, tr("admin_protected", lang), "", nil)
			return true
		}
		changed, err := a.store.banUser(a.ctx, target, message.From.ID, reason)
		if err != nil {
			a.adminStoreError(message, err)
			return true
		}
		if changed {
			a.store.audit(a.ctx, message.From.ID, "ban", target, reason)
			a.cancelUserDownloads(target)
			a.adminDone(message, "ban", target)
		} else {
			a.adminNoChange(message, target)
		}
	case "pardon", "unban":
		target, _, ok := parseAdminTarget(message.CommandArguments())
		if !ok {
			a.sendText(message.Chat.ID, tr("admin_usage", lang, "usage", "/pardon <tg_id>"), "HTML", nil)
			return true
		}
		changed, err := a.store.pardonUser(a.ctx, target)
		if err != nil {
			a.adminStoreError(message, err)
			return true
		}
		if changed {
			a.store.audit(a.ctx, message.From.ID, "pardon", target, "")
			a.adminDone(message, "pardon", target)
		} else {
			a.adminNoChange(message, target)
		}
	case "addadmin":
		target, _, ok := parseAdminTarget(message.CommandArguments())
		if !ok {
			a.sendText(message.Chat.ID, tr("admin_usage", lang, "usage", "/addadmin <tg_id>"), "HTML", nil)
			return true
		}
		if a.isOwner(target) {
			a.adminNoChange(message, target)
			return true
		}
		changed, err := a.store.addAdmin(a.ctx, target, message.From.ID)
		if err != nil {
			a.adminStoreError(message, err)
			return true
		}
		if changed {
			_, _ = a.store.pardonUser(a.ctx, target)
			a.store.audit(a.ctx, message.From.ID, "addadmin", target, "")
			registerChatCommands(a.bot, target, true)
			a.adminDone(message, "addadmin", target)
		} else {
			a.adminNoChange(message, target)
		}
	case "deladmin":
		target, _, ok := parseAdminTarget(message.CommandArguments())
		if !ok {
			a.sendText(message.Chat.ID, tr("admin_usage", lang, "usage", "/deladmin <tg_id>"), "HTML", nil)
			return true
		}
		if a.isOwner(target) {
			a.sendText(message.Chat.ID, tr("admin_protected", lang), "", nil)
			return true
		}
		changed, err := a.store.deleteAdmin(a.ctx, target)
		if err != nil {
			a.adminStoreError(message, err)
			return true
		}
		if changed {
			a.store.audit(a.ctx, message.From.ID, "deladmin", target, "")
			registerChatCommands(a.bot, target, false)
			a.adminDone(message, "deladmin", target)
		} else {
			a.adminNoChange(message, target)
		}
	case "admins":
		a.sendAdminList(message)
	case "banlist":
		a.sendBanList(message)
	case "userinfo":
		a.sendUserInfo(message)
	case "adminlog":
		a.sendAdminAudit(message)
	}
	return true
}

func (a *app) adminDone(message *tgbotapi.Message, action string, target int64) {
	a.sendText(message.Chat.ID, tr("admin_done", a.langOrDefault(message.From.ID), "action", action, "id", strconv.FormatInt(target, 10)), "HTML", nil)
}

func (a *app) adminNoChange(message *tgbotapi.Message, target int64) {
	a.sendText(message.Chat.ID, tr("admin_no_change", a.langOrDefault(message.From.ID), "id", strconv.FormatInt(target, 10)), "HTML", nil)
}

func (a *app) adminStoreError(message *tgbotapi.Message, err error) {
	a.sendText(message.Chat.ID, tr("admin_db_error", a.langOrDefault(message.From.ID), "error", html.EscapeString(err.Error())), "HTML", nil)
}

func (a *app) cancelUserDownloads(userID int64) {
	a.mu.Lock()
	var cancels []context.CancelFunc
	for _, active := range a.active {
		if active.userID == userID {
			cancels = append(cancels, active.cancel)
		}
	}
	a.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (a *app) sendAdminList(message *tgbotapi.Message) {
	lang := a.langOrDefault(message.From.ID)
	lines := []string{tr("admin_list_title", lang)}
	for _, id := range a.administratorIDs(a.ctx) {
		role := tr("admin_role_admin", lang)
		if a.isOwner(id) {
			role = tr("admin_role_owner", lang)
		}
		lines = append(lines, fmt.Sprintf("<code>%d</code> — %s", id, role))
	}
	if len(lines) == 1 {
		lines = append(lines, tr("admin_empty", lang))
	}
	a.sendText(message.Chat.ID, strings.Join(lines, "\n"), "HTML", nil)
}

func (a *app) sendBanList(message *tgbotapi.Message) {
	lang := a.langOrDefault(message.From.ID)
	page, _ := strconv.Atoi(strings.TrimSpace(message.CommandArguments()))
	if page < 1 {
		page = 1
	}
	records, err := a.store.bans(a.ctx, 20, (page-1)*20)
	if err != nil {
		a.adminStoreError(message, err)
		return
	}
	lines := []string{tr("admin_bans_title", lang, "page", strconv.Itoa(page))}
	for _, record := range records {
		reason := html.EscapeString(firstNonEmpty(record.Reason, "—"))
		lines = append(lines, fmt.Sprintf("<code>%d</code> · %s · %s", record.UserID, record.CreatedAt.Format("2006-01-02"), reason))
	}
	if len(records) == 0 {
		lines = append(lines, tr("admin_empty", lang))
	}
	a.sendText(message.Chat.ID, strings.Join(lines, "\n"), "HTML", nil)
}

func (a *app) sendUserInfo(message *tgbotapi.Message) {
	target, _, ok := parseAdminTarget(message.CommandArguments())
	if !ok {
		a.sendText(message.Chat.ID, tr("admin_usage", a.langOrDefault(message.From.ID), "usage", "/userinfo <tg_id>"), "HTML", nil)
		return
	}
	info, known, err := a.store.userInfo(a.ctx, target)
	if err != nil {
		a.adminStoreError(message, err)
		return
	}
	lang := a.langOrDefault(message.From.ID)
	role := tr("admin_role_user", lang)
	if a.isOwner(target) {
		role = tr("admin_role_owner", lang)
	} else if a.isAdmin(target) {
		role = tr("admin_role_admin", lang)
	}
	ban, banned := a.store.isBanned(a.ctx, target)
	lines := []string{fmt.Sprintf("👤 <code>%d</code>", target), tr("admin_user_line", lang, "role", role, "known", strconv.FormatBool(known), "lang", firstNonEmpty(info.Language, "—"), "downloads", strconv.FormatInt(info.Downloads, 10), "banned", strconv.FormatBool(banned))}
	if banned && ban.Reason != "" {
		lines = append(lines, tr("admin_reason_line", lang, "reason", html.EscapeString(ban.Reason)))
	}
	a.sendText(message.Chat.ID, strings.Join(lines, "\n"), "HTML", nil)
}

func (a *app) sendAdminAudit(message *tgbotapi.Message) {
	limit, _ := strconv.Atoi(strings.TrimSpace(message.CommandArguments()))
	records, err := a.store.auditLog(a.ctx, limit)
	if err != nil {
		a.adminStoreError(message, err)
		return
	}
	lines := []string{tr("admin_audit_title", a.langOrDefault(message.From.ID))}
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("%s · <code>%d</code> · %s · <code>%d</code>", record.CreatedAt.Format("01-02 15:04"), record.ActorID, html.EscapeString(record.Action), record.TargetID))
	}
	if len(records) == 0 {
		lines = append(lines, tr("admin_empty", a.langOrDefault(message.From.ID)))
	}
	a.sendText(message.Chat.ID, strings.Join(lines, "\n"), "HTML", nil)
}

func adminTraceStamp(started time.Time) string {
	return time.Since(started).Truncate(time.Millisecond).String()
}

func (a *app) handleAdminPerf(message *tgbotapi.Message) {
	if !a.requireAdmin(message, false) || a.store == nil {
		return
	}
	window := 24 * time.Hour
	label := "24h"
	switch strings.TrimSpace(message.CommandArguments()) {
	case "", "24h":
	case "1h":
		window, label = time.Hour, "1h"
	case "7d":
		window, label = 7*24*time.Hour, "7d"
	default:
		a.sendText(message.Chat.ID, tr("admin_usage", a.langOrDefault(message.From.ID), "usage", "/perf [1h|24h|7d]"), "HTML", nil)
		return
	}
	rows, err := a.store.mediaPerformance(a.ctx, time.Now().Add(-window))
	if err != nil {
		a.adminStoreError(message, err)
		return
	}
	lang := a.langOrDefault(message.From.ID)
	lines := []string{tr("admin_perf_title", lang, "window", label)}
	lines = append(lines, tr("admin_circuit", lang, "state", string(a.octaveRemote.state())))
	stats, _ := a.store.stats(a.ctx)
	totalDeliveries := stats.DownloadsOK + stats.DownloadsPartial + stats.DownloadsFailed + stats.CacheHits
	cacheRatio := int64(0)
	if totalDeliveries > 0 {
		cacheRatio = stats.CacheHits * 100 / totalDeliveries
	}
	remoteOK, remoteTotal := 0, 0
	for _, row := range rows {
		if row.Stage == "telegram_upload" && row.Mode == "remote_url" {
			remoteOK += row.OK
			remoteTotal += row.Count
		}
	}
	lines = append(lines, tr("admin_perf_summary", lang, "hits", strconv.FormatInt(stats.CacheHits, 10), "ratio", strconv.FormatInt(cacheRatio, 10), "remote_ok", strconv.Itoa(remoteOK), "remote_total", strconv.Itoa(remoteTotal)))
	for index, row := range rows {
		if index >= 12 {
			lines = append(lines, tr("admin_more_stages", lang, "count", strconv.Itoa(len(rows)-index)))
			break
		}
		success := 0
		if row.Count > 0 {
			success = row.OK * 100 / row.Count
		}
		name := row.Stage + "/" + row.Source
		if row.Mode != "" {
			name += ":" + row.Mode
		}
		line := fmt.Sprintf("<code>%s</code> · n=%d · ok=%d%% · p50=%s · p95=%s", html.EscapeString(name), row.Count, success, shortDuration(row.P50), shortDuration(row.P95))
		if row.Throughput > 0 {
			line += " · " + humanSize(row.Throughput, lang) + "/s"
		}
		lines = append(lines, line)
	}
	if len(rows) == 0 {
		lines = append(lines, tr("admin_empty", lang))
	}
	if recorder := activeMediaRecorder.Load(); recorder != nil && recorder.dropped.Load() > 0 {
		lines = append(lines, tr("admin_dropped_samples", lang, "count", strconv.FormatInt(recorder.dropped.Load(), 10)))
	}
	a.sendText(message.Chat.ID, strings.Join(lines, "\n"), "HTML", nil)
}

func shortDuration(value time.Duration) string {
	if value < time.Second {
		return strconv.FormatInt(value.Milliseconds(), 10) + "ms"
	}
	return value.Truncate(100 * time.Millisecond).String()
}
