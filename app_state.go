package main

import "time"

func (a *app) getLang(userID int64) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	lang, ok := a.userLang[userID]
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
}

func (a *app) storeURL(request pendingURL) (string, error) {
	key, err := randomID()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
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
	if !ok || request.UserID != userID || request.ChatID != chatID {
		return pendingURL{}, false
	}
	delete(a.urls, key)
	return request, true
}

func (a *app) storeZIP(request zipRequest) (string, error) {
	key, err := randomID()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.pendingZIP[key] = request
	a.zipOrder = append(a.zipOrder, key)
	var evicted []zipRequest
	for len(a.zipOrder) > maxStoredEntries {
		oldKey := a.zipOrder[0]
		a.zipOrder = a.zipOrder[1:]
		if old, ok := a.pendingZIP[oldKey]; ok {
			evicted = append(evicted, old)
		}
		delete(a.pendingZIP, oldKey)
	}
	a.mu.Unlock()
	for _, old := range evicted {
		if len(old.Results) > 0 {
			a.downloader.clearSession(old.Results[0].Session)
		}
	}
	time.AfterFunc(pendingZIPTTL, func() { a.expireZIP(key) })
	return key, nil
}

func (a *app) expireZIP(key string) {
	request, ok := a.removeZIP(key)
	if ok && len(request.Results) > 0 {
		a.downloader.clearSession(request.Results[0].Session)
	}
}

func (a *app) popZIP(key string, userID, chatID int64) (zipRequest, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	request, ok := a.pendingZIP[key]
	if !ok || request.UserID != userID || request.ChatID != chatID {
		return zipRequest{}, false
	}
	delete(a.pendingZIP, key)
	return request, true
}

func (a *app) removeZIP(key string) (zipRequest, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	request, ok := a.pendingZIP[key]
	delete(a.pendingZIP, key)
	return request, ok
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
