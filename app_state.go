package main

import "time"

func (a *app) getLang(userID int64) (string, bool) {
	a.mu.Lock()
	lang, ok := a.userLang[userID]
	a.mu.Unlock()
	if !ok && a.store != nil {
		lang, ok = a.store.language(a.ctx, userID)
		if ok {
			a.mu.Lock()
			a.userLang[userID] = lang
			a.mu.Unlock()
		}
	}
	return lang, ok
}

func (a *app) langOrDefault(userID int64) string {
	if lang, ok := a.getLang(userID); ok {
		return lang
	}
	return defaultLang
}

func (a *app) setLang(userID int64, lang string) {
	a.mu.Lock()
	a.userLang[userID] = lang
	a.mu.Unlock()
	if a.store != nil {
		if err := a.store.setLanguage(a.ctx, userID, lang); err != nil {
			// The in-memory value still keeps the bot usable if storage is temporarily busy.
			return
		}
	}
}

func (a *app) getURL(key string, userID, chatID int64) (pendingURL, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	request, ok := a.urls[key]
	if !ok || request.UserID != userID || request.ChatID != chatID || time.Now().After(request.ExpiresAt) {
		if ok && time.Now().After(request.ExpiresAt) {
			delete(a.urls, key)
		}
		return pendingURL{}, false
	}
	return request, true
}

func (a *app) setURLRange(key string, userID, chatID int64, start, end int) (pendingURL, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	request, ok := a.urls[key]
	if !ok || request.UserID != userID || request.ChatID != chatID || time.Now().After(request.ExpiresAt) {
		if ok && time.Now().After(request.ExpiresAt) {
			delete(a.urls, key)
		}
		return pendingURL{}, false
	}
	request.RangeStart, request.RangeEnd = start, end
	a.urls[key] = request
	return request, true
}

func (a *app) setURLDelivery(key string, userID, chatID int64, delivery string) (pendingURL, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	request, ok := a.urls[key]
	if !ok || request.UserID != userID || request.ChatID != chatID || time.Now().After(request.ExpiresAt) {
		if ok && time.Now().After(request.ExpiresAt) {
			delete(a.urls, key)
		}
		return pendingURL{}, false
	}
	request.Delivery = delivery
	a.urls[key] = request
	return request, true
}

func (a *app) beginUserDownload(userID int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeUser[userID] {
		return false
	}
	a.activeUser[userID] = true
	return true
}

func (a *app) finishUserDownload(userID int64) {
	a.mu.Lock()
	delete(a.activeUser, userID)
	a.mu.Unlock()
}

func (a *app) storeURL(request pendingURL) (string, error) {
	key, err := randomID()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if request.ExpiresAt.IsZero() {
		request.ExpiresAt = time.Now().Add(pendingURLTTL)
	}
	a.urls[key] = request
	a.urlOrder = append(a.urlOrder, key)
	for len(a.urlOrder) > maxStoredEntries {
		delete(a.urls, a.urlOrder[0])
		a.urlOrder = a.urlOrder[1:]
	}
	return key, nil
}

func (a *app) popURL(key string, userID, chatID int64) (pendingURL, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	request, ok := a.urls[key]
	if !ok || request.UserID != userID || request.ChatID != chatID || time.Now().After(request.ExpiresAt) {
		if ok && time.Now().After(request.ExpiresAt) {
			delete(a.urls, key)
		}
		return pendingURL{}, false
	}
	delete(a.urls, key)
	return request, true
}

func (a *app) restoreURL(key string, request pendingURL) {
	a.mu.Lock()
	if _, exists := a.urls[key]; !exists && time.Now().Before(request.ExpiresAt) {
		a.urls[key] = request
	}
	a.mu.Unlock()
}

func (a *app) getActiveDownload(key string, userID, chatID int64) (activeDownload, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	download, ok := a.active[key]
	if !ok || download.userID != userID || download.chatID != chatID {
		return activeDownload{}, false
	}
	return download, true
}
