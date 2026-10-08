"""Read current Newgrounds audio pages through yt-dlp's normal download pipeline."""

from urllib.parse import urlsplit

from yt_dlp.extractor.newgrounds import NewgroundsIE
from yt_dlp.utils import ExtractorError, int_or_none


class NewgroundsAudioIE(NewgroundsIE):
    IE_NAME = "Newgrounds:audio"
    _VALID_URL = r"https?://(?:www\.)?newgrounds\.com/audio/listen/(?P<id>\d+)(?:[/?#]|$)"

    def _real_extract(self, url):
        media_id = self._match_id(url)
        webpage = self._download_webpage(url, media_id)
        audio_url = self._html_search_meta("og:audio", webpage, default=None)
        if not audio_url:
            return super()._real_extract(url)

        parsed = urlsplit(audio_url)
        if (
            parsed.scheme != "https"
            or parsed.hostname != "audio.ngfiles.com"
            or parsed.username is not None
            or parsed.password is not None
            or parsed.port is not None
        ):
            raise ExtractorError("Unsafe Newgrounds audio URL", expected=True)

        uploader = self._html_search_regex(
            (
                r"(?s)<h4[^>]*>((?:(?!</h4>).)+)</h4>(?:(?!<h4).)*?<em>\s*(?:Author|Artist)\s*</em>",
                r"(?:Author|Writer)\s*<a[^>]+>([^<]+)",
            ),
            webpage,
            "uploader",
            fatal=False,
        )
        duration = int_or_none(self._search_regex(
            r"['\"]duration['\"]\s*:\s*['\"]?(\d+)",
            webpage,
            "duration",
            default=None,
        ))
        thumbnail = self._og_search_thumbnail(webpage, default=None)
        if thumbnail:
            image = urlsplit(thumbnail)
            if image.scheme != "https" or image.hostname != "aicon.ngfiles.com" or image.username is not None or image.password is not None or image.port is not None:
                thumbnail = None

        return {
            "id": media_id,
            "title": self._og_search_title(webpage, default=None) or self._html_extract_title(webpage),
            "uploader": uploader,
            "duration": duration,
            "thumbnail": thumbnail,
            "formats": [{
                "url": audio_url,
                "format_id": "source",
                "ext": "mp3",
                "vcodec": "none",
                "acodec": "mp3",
            }],
            "http_headers": {"Referer": url},
        }
