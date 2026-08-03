"""propscope collector -- fills postgres with HF propagation measurements.

One process, one asyncio task per source, each on its own interval, sharing one
HTTP session and one small postgres pool. Deliberately not a cron fleet: N
python interpreters waking up on the minute costs far more than one long-lived
process that sleeps.

Endpoints (bound to localhost inside the container; compose does not publish
them, they exist for the healthcheck and for debugging):
    GET /health       -- liveness + which sources are degraded
    GET /api/sources  -- what is configured and how it is doing

Design rules that matter:
  * one source failing must never stop the others
  * every run is recorded in collector_runs, success or failure
  * intervals are jittered so sources never fire in the same second
  * a source's first run is staggered, so start-up does not stampede
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import logging
import os
import random
import signal

import aiohttp
from aiohttp import web

from . import db, notify
from .sources import Source, enabled

logging.basicConfig(
    level=os.environ.get("LOG_LEVEL", "INFO").upper(),
    format="%(asctime)s %(levelname)-7s %(name)-22s %(message)s",
)
log = logging.getLogger("collector")

# Identify yourself. These are free services run by volunteers and government
# agencies; an anonymous scraper is the first thing they block.
USER_AGENT = os.environ.get(
    "COLLECTOR_UA",
    "propscope-collector/1.0 (+https://github.com/; amateur radio propagation)",
)

STATE: dict[str, dict] = {}


async def run_source(source: Source, session: aiohttp.ClientSession) -> None:
    st = STATE.setdefault(source.name, {})
    run_id = None
    try:
        run_id = await db.run_start(source.name)
        rows = await source.fetch(session)
        await db.run_finish(run_id, True, rows or 0, None)
        st.update(ok=True, rows=rows or 0, error=None)
        log.info("%s: ok, %s new row(s)", source.name, rows)
    except asyncio.CancelledError:
        raise
    except Exception as e:
        msg = f"{type(e).__name__}: {e}"
        st.update(ok=False, error=msg)
        log.warning("%s: FAILED %s", source.name, msg)
        if run_id is not None:
            with contextlib.suppress(Exception):
                await db.run_finish(run_id, False, 0, msg)
        st["fails"] = st.get("fails", 0) + 1
        if st["fails"] in (5, 25):
            await notify.push(
                title=f"collector: {source.name} failing",
                message=f"{st['fails']} consecutive failures. Last error: {msg}",
                tags="warning",
                priority=3,
                dedupe_key=f"src-fail-{source.name}-{st['fails']}",
            )
    else:
        st["fails"] = 0


async def source_loop(source: Source, session: aiohttp.ClientSession) -> None:
    # Stagger first runs: every source hitting the network at t=0 just queues
    # them behind each other anyway.
    await asyncio.sleep(random.uniform(0, min(30, source.interval / 2)))
    while True:
        await run_source(source, session)
        # +/-10% jitter so intervals never resonate into a synchronised burst.
        await asyncio.sleep(source.interval * random.uniform(0.9, 1.1))


async def housekeeping() -> None:
    while True:
        await asyncio.sleep(6 * 3600)
        with contextlib.suppress(Exception):
            await db.prune_runs(keep_days=14)


def _json(data, status=200):
    return web.Response(status=status, text=json.dumps(data, default=str, indent=1),
                        content_type="application/json")


async def h_health(request):
    unhealthy = [n for n, s in STATE.items() if s.get("fails", 0) >= 5]
    return _json({"healthy": not unhealthy, "sources": len(enabled()),
                  "degraded": unhealthy}, status=200 if not unhealthy else 503)


async def h_sources(request):
    return _json([{
        "name": s.name,
        "description": s.description,
        "interval_s": s.interval,
        "tags": s.tags,
        "ok": STATE.get(s.name, {}).get("ok"),
        "last_rows": STATE.get(s.name, {}).get("rows"),
        "consecutive_failures": STATE.get(s.name, {}).get("fails", 0),
        "error": STATE.get(s.name, {}).get("error"),
    } for s in enabled()])


def make_app() -> web.Application:
    app = web.Application()
    app.router.add_get("/health", h_health)
    app.router.add_get("/api/sources", h_sources)
    app.router.add_get("/", h_health)
    return app


async def amain() -> None:
    srcs = enabled()
    log.info("starting with %d source(s): %s", len(srcs), ", ".join(s.name for s in srcs))

    await db.init()
    for s in srcs:
        try:
            await db.execute(s.ddl)
        except Exception as e:
            log.error("DDL failed for %s: %s", s.name, e)

    timeout = aiohttp.ClientTimeout(total=60, connect=15)
    # One connection per host at a time: we are a guest on volunteer-run APIs.
    conn = aiohttp.TCPConnector(limit=4, limit_per_host=1, ttl_dns_cache=300)
    session = aiohttp.ClientSession(timeout=timeout, connector=conn,
                                    headers={"User-Agent": USER_AGENT})

    runner = web.AppRunner(make_app(), access_log=None)
    await runner.setup()
    await web.TCPSite(runner, "0.0.0.0", 8000).start()
    log.info("http api on :8000")

    tasks = [asyncio.create_task(source_loop(s, session), name=s.name) for s in srcs]
    tasks.append(asyncio.create_task(housekeeping(), name="housekeeping"))

    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        with contextlib.suppress(NotImplementedError):
            loop.add_signal_handler(sig, stop.set)
    await stop.wait()

    log.info("shutting down")
    for t in tasks:
        t.cancel()
    await asyncio.gather(*tasks, return_exceptions=True)
    await session.close()
    await runner.cleanup()
    await db.close()


if __name__ == "__main__":
    with contextlib.suppress(KeyboardInterrupt):
        asyncio.run(amain())
