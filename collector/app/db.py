"""Postgres access for the collector.

One shared connection pool. Every source owns its own table and declares the
DDL itself, so adding a source never needs a migration step.
"""

from __future__ import annotations

import logging
import os

from psycopg.types.json import Json
from psycopg_pool import AsyncConnectionPool

# Re-exported so a source can write a jsonb column without importing psycopg
# itself: db.Json(some_dict). Sources only ever talk to this module.
__all__ = ["Json", "close", "execute", "executemany", "fetch", "fetchone",
           "init", "pool", "prune_runs", "run_finish", "run_start"]

log = logging.getLogger("collector.db")

_pool: AsyncConnectionPool | None = None


def dsn() -> str:
    # timezone=UTC is load-bearing, not cosmetic.
    #
    # This container runs TZ=America/Chicago, and postgres resolves a NAIVE
    # timestamp using the session's TimeZone. Every upstream this collector
    # reads publishes UTC, and most of them publish it naive -- wspr.live
    # returns "2026-08-03 02:40:00", SWPC returns "2026-08-02T20:00:00". With a
    # Chicago session those were being stored five hours off, which put WSPR
    # buckets in the FUTURE and silently broke any "recent data" query.
    #
    # Pinning the session to UTC fixes the whole class: naive in, UTC assumed.
    # Sources that build their own aware datetimes are unaffected either way.
    return (
        f"host={os.environ.get('PGHOST', 'postgres')} "
        f"port={os.environ.get('PGPORT', '5432')} "
        f"dbname={os.environ.get('PGDATABASE', 'propscope')} "
        f"user={os.environ.get('PGUSER', 'propscope')} "
        f"password={os.environ['PGPASSWORD']} "
        f"connect_timeout=10 "
        f"options='-c timezone=UTC' "
        f"application_name=propscope-collector"
    )


async def pool() -> AsyncConnectionPool:
    global _pool
    if _pool is None:
        # min_size=1: this box has one core, an idle pool of connections is pure
        # overhead and postgres here is capped at 60 connections total.
        _pool = AsyncConnectionPool(dsn(), min_size=1, max_size=4, open=False, timeout=20)
        await _pool.open(wait=True, timeout=30)
        log.info("postgres pool open")
    return _pool


async def close() -> None:
    global _pool
    if _pool is not None:
        await _pool.close()
        _pool = None


async def execute(sql: str, params: tuple | None = None) -> None:
    p = await pool()
    async with p.connection() as con:
        await con.execute(sql, params)


async def executemany(sql: str, rows: list[tuple]) -> int:
    """Returns rows actually written. With ON CONFLICT DO NOTHING that is much
    smaller than len(rows), and the difference is exactly what we want to log."""
    if not rows:
        return 0
    p = await pool()
    async with p.connection() as con:
        cur = con.cursor()
        await cur.executemany(sql, rows)
        affected = cur.rowcount
    return affected if affected is not None and affected >= 0 else len(rows)


async def fetch(sql: str, params: tuple | None = None) -> list[tuple]:
    p = await pool()
    async with p.connection() as con:
        cur = await con.execute(sql, params)
        return await cur.fetchall()


async def fetchone(sql: str, params: tuple | None = None):
    rows = await fetch(sql, params)
    return rows[0] if rows else None


RUNS_DDL = """
CREATE TABLE IF NOT EXISTS collector_runs (
    id          bigserial PRIMARY KEY,
    source      text        NOT NULL,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    ok          boolean,
    rows_new    integer     NOT NULL DEFAULT 0,
    error       text
);
CREATE INDEX IF NOT EXISTS collector_runs_source_started
    ON collector_runs (source, started_at DESC);
"""


async def init() -> None:
    await execute(RUNS_DDL)


async def run_start(source: str) -> int:
    row = await fetchone(
        "INSERT INTO collector_runs (source) VALUES (%s) RETURNING id", (source,)
    )
    return row[0]


async def run_finish(run_id: int, ok: bool, rows_new: int, error: str | None) -> None:
    await execute(
        "UPDATE collector_runs SET finished_at = now(), ok = %s, rows_new = %s, error = %s "
        "WHERE id = %s",
        (ok, rows_new, (error or None), run_id),
    )


async def prune_runs(keep_days: int = 14) -> None:
    """The run log is diagnostics, not data. Do not let it grow forever."""
    await execute(
        "DELETE FROM collector_runs WHERE started_at < now() - make_interval(days => %s)",
        (keep_days,),
    )
