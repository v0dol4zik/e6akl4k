"""Offline checks of the VK Music extractor's bootstrap retry logic (no network).

Runs with a stub ``yt_dlp`` package so the adapter can be imported outside the release image.
"""
import ast
import json
import importlib.util
import sys
import types
from pathlib import Path

SRC = Path(sys.argv[1]).read_text()
ast.parse(SRC)


class ExtractorError(Exception):
    def __init__(self, msg, expected=False):
        super().__init__(msg)
        self.expected = expected


def js_to_json(text):  # the adapter's pages are already JSON in this harness
    return text


def int_or_none(value):
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def clean_html(value):
    return value


def urlencode_postdata(data):
    return "&".join(f"{k}={v}" for k, v in data.items()).encode()


yt_dlp = types.ModuleType("yt_dlp")
extractor = types.ModuleType("yt_dlp.extractor")
common = types.ModuleType("yt_dlp.extractor.common")
utils = types.ModuleType("yt_dlp.utils")
utils.ExtractorError = ExtractorError
utils.clean_html = clean_html
utils.int_or_none = int_or_none
utils.js_to_json = js_to_json
utils.urlencode_postdata = urlencode_postdata
class _IE:
    def __init__(self, downloader=None):
        pass

    def _search_json(self, pattern, string, name, video_id, transform_source=None, default=...):
        try:
            return json.loads(transform_source(string) if transform_source else string)
        except ValueError:
            if default is ...:
                raise ExtractorError("no state")
            return default


common.InfoExtractor = _IE
sys.modules.update({"yt_dlp": yt_dlp, "yt_dlp.extractor": extractor,
                    "yt_dlp.extractor.common": common, "yt_dlp.utils": utils})

spec = importlib.util.spec_from_loader("vk_music", loader=None)
module = importlib.util.module_from_spec(spec)
exec(compile(SRC, "vk_music_extractor.py", "exec"), module.__dict__)

GOOD = '{"id": 1, "webToken": {"access_token": "t"}, "apiConfigDomains": {"apiDomain": "web.api.vk.ru"}}'
OTHER_API = '{"id": 1, "webToken": {"access_token": "t"}, "apiConfigDomains": {"apiDomain": "api.vk.ru"}}'
GUEST = '{"id": 0, "webToken": {"is_anonym": true}}'


def run(pages):
    page_list = list(pages)
    instance = module.VKMusicIE()
    instance._get_cookies = lambda origin: [object()]
    instance._set_cookie = lambda *args: None
    calls = []

    def download(url, video_id, note=None, headers=None, **kwargs):
        calls.append(note)
        return page_list.pop(0) if page_list else GOOD

    instance._download_webpage = download
    outcome = "ok"
    try:
        instance._bootstrap("1_1")
    except ExtractorError as error:
        outcome = str(error)
    return outcome, len(calls), instance._api_domain if outcome == "ok" else None


cases = [
    ("regular page", [GOOD], "ok", 1),
    ("other API domain, then regular", [OTHER_API, GOOD], "ok", 2),
    ("guest page, then regular", [GUEST, GOOD], "ok", 2),
    ("three other-API pages, then regular", [OTHER_API, OTHER_API, OTHER_API, GOOD], "ok", 4),
    ("always guest", [GUEST] * 6, "vk_music:session", 4),
    ("always other API domain", [OTHER_API] * 6, "vk_music:protocol", 4),
    ("mixed guest and other API, never regular", [GUEST, OTHER_API] * 3, "vk_music:protocol", 4),
]
failures = 0
for name, pages, want, want_calls in cases:
    outcome, calls, api = run(pages)
    ok = outcome == want and calls == want_calls and (api is None or api == "web.api.vk.ru")
    failures += not ok
    print(("PASS" if ok else "FAIL"), name, "->", outcome, f"requests={calls}", f"api={api}")
print("attempts setting:", module._VKMusicBaseIE._BOOTSTRAP_ATTEMPTS)
sys.exit(1 if failures else 0)
