"""Return only recognition metadata; audio decoding is bounded by the Go caller."""

import asyncio
import json
import sys


async def recognize(path):
    from aiohttp_retry import ExponentialRetry
    from shazamio import HTTPClient, Shazam

    class RecognitionHTTPClient(HTTPClient):
        async def request(self, method, url, *args, **kwargs):
            # A JSON error response from the service is not an unrecognized song.
            kwargs["raise_for_status"] = True
            return await super().request(method, url, *args, **kwargs)

    client = RecognitionHTTPClient(
        retry_options=ExponentialRetry(attempts=2, max_timeout=2, statuses={429, 500, 502, 503, 504})
    )
    result = await asyncio.wait_for(Shazam(http_client=client).recognize(path), timeout=35)
    if not isinstance(result, dict):
        return {"status": "error"}
    track = result.get("track")
    if not isinstance(track, dict) or not track.get("title"):
        return {"status": "not_found"}
    title = track.get("title")
    artist = track.get("subtitle", "")
    if not isinstance(title, str) or not isinstance(artist, str):
        return {"status": "error"}
    return {"status": "ok", "title": title.strip()[:200], "artist": artist.strip()[:200]}


def main():
    # shazamio-core 1.1.2 can crash on import under Python 3.14.
    if not (3, 10) <= sys.version_info[:2] < (3, 14):
        print(json.dumps({"status": "unavailable"}))
        return
    try:
        result = asyncio.run(recognize(sys.argv[1]))
    except ImportError:
        result = {"status": "unavailable"}
    except TimeoutError:
        result = {"status": "timeout"}
    except Exception:
        # Paths, signed URLs and provider responses must never enter bot messages or logs.
        result = {"status": "error"}
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
