"""VK Music web session adapter for the pinned yt-dlp runtime.

The mobile page issues a web token for web.api.vk.ru, not api.vk.ru.
Tokens and playable URLs stay inside this extraction process.
"""

import hashlib
import re
from urllib.parse import urlsplit

from yt_dlp.extractor.common import InfoExtractor
from yt_dlp.utils import ExtractorError, clean_html, int_or_none, js_to_json, urlencode_postdata


class _VKMusicBaseIE(InfoExtractor):
    _API_VERSION = "5.289"
    _MOBILE_UA = (
        "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"
    )
    _MEDIA_DOMAINS = ("vkuseraudio.ru", "vkuseraudio.net", "userapi.com")
    _IMAGE_DOMAINS = ("userapi.com", "vkuserphoto.ru", "vkuserphoto.net")
    _MAX_TRACKS = 1000

    @staticmethod
    def _fail(code):
        raise ExtractorError(f"vk_music:{code}", expected=True)

    @staticmethod
    def _safe_url(raw, domains):
        if not isinstance(raw, str):
            return None
        try:
            parsed = urlsplit(raw)
            host = parsed.hostname or ""
            if (
                parsed.scheme != "https"
                or parsed.username is not None
                or parsed.password is not None
                or parsed.port is not None
                or not any(host == domain or host.endswith("." + domain) for domain in domains)
            ):
                return None
        except ValueError:
            return None
        return raw

    def _bootstrap(self, media_id):
        self._origin = "https://m.vk.ru"
        if not self._get_cookies(self._origin) and self._get_cookies("https://m.vk.com"):
            self._origin = "https://m.vk.com"
        domain = "." + urlsplit(self._origin).hostname.removeprefix("m.")
        self._set_cookie(domain, "remixaudio_show_alert_today", "0")
        self._set_cookie(domain, "remixmdevice", "1920/1080/2/!!-!!!!")
        page = self._download_webpage(
            self._origin + "/", media_id, note="Opening VK Music session",
            headers={"User-Agent": self._MOBILE_UA},
        )
        state = self._search_json(
            r"\b(?:window\.)?vk\s*=", page, "VK session", media_id,
            transform_source=js_to_json, default={},
        )
        token = state.get("webToken")
        if not isinstance(token, dict) or not int_or_none(state.get("id")) or token.get("is_anonym"):
            self._fail("session")
        self._token = token.get("access_token")
        domain = (state.get("apiConfigDomains") or {}).get("apiDomain")
        if domain not in ("web.api.vk.ru", "web.api.vk.com") or not isinstance(self._token, str) or not self._token:
            self._fail("protocol")
        self._api_domain = domain

    def _api(self, method, media_id, fields, refresh=True):
        payload = self._download_json(
            f"https://{self._api_domain}/method/{method}", media_id,
            note=f"Reading VK Music ({method})",
            data=urlencode_postdata({**fields, "v": self._API_VERSION, "access_token": self._token}),
            headers={
                "User-Agent": self._MOBILE_UA,
                "Origin": self._origin,
                "Referer": self._origin + "/",
                "Content-Type": "application/x-www-form-urlencoded",
            },
        )
        if not isinstance(payload, dict):
            self._fail("protocol")
        error = payload.get("error")
        if error:
            code = int_or_none(error.get("error_code")) if isinstance(error, dict) else None
            if code in (5, 27, 28):
                if refresh:
                    self._bootstrap(media_id)
                    return self._api(method, media_id, fields, refresh=False)
                self._fail("session")
            if code in (6, 9, 29):
                self._fail("rate_limit")
            if code in (14, 17, 24, 25):
                self._fail("challenge")
            if code in (15, 18, 100, 200, 201):
                self._fail("unavailable")
            self._fail("protocol")
        if "response" not in payload:
            self._fail("protocol")
        return payload["response"]

    @staticmethod
    def _source_id(row):
        if not isinstance(row, dict):
            return None
        owner, audio = int_or_none(row.get("owner_id")), int_or_none(row.get("id"))
        if not owner or not audio or audio < 0:
            return None
        return f"{owner}_{audio}"

    def _metadata(self, row):
        source_id = self._source_id(row)
        if not source_id:
            self._fail("protocol")
        title = clean_html(row.get("title")) or "VK Music"
        artist = clean_html(row.get("artist"))
        duration = int_or_none(row.get("duration"))
        access = row.get("access_key")
        access = access if isinstance(access, str) and re.fullmatch(r"[A-Za-z0-9-]{1,128}", access) else ""
        page = f"https://vk.ru/audio{source_id}" + (f"_{access}" if access else "")
        thumb = row.get("thumb") or (row.get("album") or {}).get("thumb") or {}
        thumbnail = None
        if isinstance(thumb, dict):
            for size in (1200, 600, 300, 270, 135, 68, 34):
                thumbnail = self._safe_url(thumb.get(f"photo_{size}"), self._IMAGE_DOMAINS)
                if thumbnail:
                    break
        return {
            "id": source_id,
            "title": title,
            "track": title,
            "artist": artist,
            "uploader": artist,
            "duration": duration,
            "thumbnail": thumbnail,
            "webpage_url": page,
        }

    def _track_result(self, row):
        result = self._metadata(row)
        if any(row.get(key) for key in ("is_preview", "isPreview", "is_partial", "isPartial")):
            self._fail("preview_only")
        if not result["duration"] or result["duration"] < 1:
            self._fail("protocol")
        raw = row.get("url")
        if not raw:
            self._fail("unavailable")
        media_url = self._safe_url(raw, self._MEDIA_DOMAINS)
        if not media_url:
            self._fail("unsafe_url")
        path = urlsplit(media_url).path
        if path.endswith(".m3u8"):
            formats = self._extract_m3u8_formats(
                media_url, result["id"], ext="m4a", m3u8_id="hls",
                headers={"Referer": self._origin + "/", "User-Agent": self._MOBILE_UA},
            )
            for fmt in formats:
                if not self._safe_url(fmt.get("url"), self._MEDIA_DOMAINS):
                    self._fail("unsafe_url")
                fmt["vcodec"] = "none"
        elif path.endswith((".mp3", ".m4a", ".aac", ".ogg", ".opus")):
            formats = [{"url": media_url, "format_id": "source", "ext": path.rsplit(".", 1)[1], "vcodec": "none"}]
        else:
            self._fail("protocol")
        result["formats"] = formats
        result["http_headers"] = {"Referer": self._origin + "/", "User-Agent": self._MOBILE_UA}
        return result


class VKMusicIE(_VKMusicBaseIE):
    IE_NAME = "vk_music"
    _VALID_URL = (
        r"https?://(?:(?:www|m|music)\.)?vk\.(?:ru|com)/audio"
        r"(?P<id>-?[1-9]\d*_[1-9]\d*)(?:_(?P<access>[A-Za-z0-9-]{1,128}))?(?:[/?#]|$)"
    )

    def _real_extract(self, url):
        match = self._match_valid_url(url)
        media_id, access = match.group("id", "access")
        self._bootstrap(media_id)
        rows = self._api("audio.getById", media_id, {"audios": media_id + (f"_{access}" if access else "")})
        if not isinstance(rows, list):
            self._fail("protocol")
        row = next((row for row in rows if self._source_id(row) == media_id), None)
        if row is None:
            self._fail("unavailable")
        return self._track_result(row)


class VKMusicPlaylistIE(_VKMusicBaseIE):
    IE_NAME = "vk_music:playlist"
    _VALID_URL = (
        r"https?://(?:(?:www|m|music)\.)?vk\.(?:ru|com)/(?:music/album/|audio_playlist)"
        r"(?P<id>(?P<owner>-?[1-9]\d*)_(?P<album>[1-9]\d*))(?:_(?P<access>[A-Za-z0-9-]{1,128}))?(?:[/?#]|$)"
    )

    def _real_extract(self, url):
        match = self._match_valid_url(url)
        media_id, owner, album, access = match.group("id", "owner", "album", "access")
        self._bootstrap(media_id)
        fields = {"owner_id": owner, "playlist_id": album}
        if access:
            fields["access_key"] = access
        album_info = self._api("audio.getPlaylistById", media_id, fields)
        if not isinstance(album_info, dict):
            self._fail("protocol")
        title = clean_html(album_info.get("title")) or "VK Music"
        entries = []
        offset = 0
        while True:
            fields = {"owner_id": owner, "album_id": album, "offset": offset, "count": 100}
            if access:
                fields["access_key"] = access
            page = self._api("audio.get", media_id, fields)
            if not isinstance(page, dict) or not isinstance(page.get("items"), list):
                self._fail("protocol")
            count = int_or_none(page.get("count"))
            rows = page["items"]
            if count is None or count < 0:
                self._fail("protocol")
            if count > self._MAX_TRACKS or offset + len(rows) > self._MAX_TRACKS:
                self._fail("too_large")
            if not rows and offset < count:
                self._fail("protocol")
            for row in rows:
                if not self._source_id(row):
                    self._fail("protocol")
                info = self._metadata(row)
                entries.append(self.url_result(
                    info["webpage_url"], VKMusicIE, video_id=info["id"],
                    video_title=info["title"], artist=info["artist"],
                    duration=info["duration"], thumbnail=info["thumbnail"],
                ))
            offset += len(rows)
            if offset >= count:
                break
        return self.playlist_result(entries, media_id, title)
