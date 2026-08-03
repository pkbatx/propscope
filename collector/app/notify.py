"""ntfy push, with de-duplication.

The collector polls on short intervals, and most feeds re-serve the same data
every poll. Without a dedupe memo a single geomagnetic storm would push a
notification every 15 minutes for days.
"""

from __future__ import annotations

import logging
import os
import time

import aiohttp

log = logging.getLogger("collector.notify")

_seen: dict[str, float] = {}
# One push per key per 6 hours.
DEDUPE_TTL = 6 * 3600


def _url() -> str | None:
    base = os.environ.get("NTFY_URL", "http://ntfy")
    topic = os.environ.get("NTFY_ALERT_TOPIC", "").strip()
    if not topic:
        return None
    return f"{base.rstrip('/')}/{topic}"


async def push(title: str, message: str, tags: str = "", priority: int = 3,
               dedupe_key: str | None = None) -> bool:
    url = _url()
    if not url:
        return False

    if dedupe_key:
        now = time.time()
        # Opportunistic cleanup; this dict never gets large.
        for k, ts in list(_seen.items()):
            if now - ts > DEDUPE_TTL:
                _seen.pop(k, None)
        if dedupe_key in _seen:
            return False
        _seen[dedupe_key] = now

    headers = {"Title": title, "Priority": str(priority)}
    if tags:
        headers["Tags"] = tags
    token = os.environ.get("NTFY_TOKEN", "").strip()
    if token:
        headers["Authorization"] = f"Bearer {token}"

    try:
        timeout = aiohttp.ClientTimeout(total=15)
        async with aiohttp.ClientSession(timeout=timeout) as s:
            async with s.post(url, data=message.encode(), headers=headers) as r:
                if r.status >= 300:
                    body = (await r.text())[:200]
                    log.warning("ntfy push failed HTTP %s: %s", r.status, body)
                    return False
        log.info("pushed: %s", title)
        return True
    except Exception as e:
        # A failed notification must never take down a collection run.
        log.warning("ntfy push error: %s", e)
        return False
