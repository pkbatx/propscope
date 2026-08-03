"""Collector sources.

A source is a small object with:

    name      -- stable identifier, also the collector_runs key
    interval  -- seconds between runs (respect the upstream's rate limits!)
    ddl       -- CREATE TABLE IF NOT EXISTS ... , run before the first fetch
    fetch(s)  -- async, takes an aiohttp.ClientSession, returns rows_new (int)

Rate limiting is a per-source responsibility and it is not optional. Every
upstream here is a free public service -- NOAA SWPC, a volunteer-run WSPR
mirror, and one person's ionosonde aggregator. Polling them harder than the
intervals below is how a single datacenter IP gets banned for everyone.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Awaitable, Callable

log = logging.getLogger("collector.sources")


@dataclass
class Source:
    name: str
    interval: int
    ddl: str
    fetch: Callable[..., Awaitable[int]]
    description: str = ""
    # Set False to keep a source defined but idle.
    enabled: bool = True
    tags: list[str] = field(default_factory=list)


from . import ionosonde, solar, wspr  # noqa: E402

SOURCES: list[Source] = [
    # solar     -- the drivers (flux, sunspot number, Kp, effective SSN)
    # wspr      -- measured band activity, spots per band
    # ionosonde -- measured ionospheric state (foF2/MUF) + local absorption
    *solar.SOURCES,
    *wspr.SOURCES,
    *ionosonde.SOURCES,
]


def enabled() -> list[Source]:
    return [s for s in SOURCES if s.enabled]
