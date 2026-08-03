"""Solar indices -- the drivers behind HF propagation.

Four feeds, all verified live on 2026-08-03. Response shapes are exact, not
assumed; do not "tidy" a parser here without re-checking the upstream first.

  services.swpc.noaa.gov/products/summary/10cm-flux.json
      [{"flux":127,"time_tag":"2026-08-02T20:00:00"}]
      A single-element summary -- the CURRENT value only, no history.

  services.swpc.noaa.gov/products/noaa-planetary-k-index.json
      [{"time_tag":"2026-07-18T00:00:00","Kp":1.67,"a_running":6,
        "station_count":8}, ...]
      ASCENDING by time, so the newest sample is the LAST element.

  services.swpc.noaa.gov/text/daily-solar-indices.txt   (the DSD report)
      Fixed-width text, 30 days of history, one row per UTC day:
        2026 08 02  127    105      360      0    -999      *   3  0  0  2 ...
        YYYY MM DD  flux   SSN     area    new    field   bkgd  C  M  X  S
      This is the only place SWPC publishes a DAILY sunspot number as JSON-free
      text; the solar-cycle JSON products are MONTHLY and far too coarse to
      answer "what is the sun doing today". Missing numerics are -999, and the
      X-ray background column is a literal "*" when absent.

  prop.kc2g.com/api/essn.json
      {"24h":[{"time":1785120903,"ssn":79.58,"sfi":122.25}, ...],
       "6h":[...], "diffusion":[]}
      EFFECTIVE sunspot number -- back-fitted from real ionosonde soundings
      rather than counted optically, at ~15 minute resolution with roughly six
      days of history. For predicting whether a band will open this is strictly
      better than the optical SSN above, because it describes the ionosphere
      that exists rather than the spots that caused it. `time` is unix seconds.
      "diffusion" is empty in practice and is ignored.

Both hosts are free public services. The intervals below are deliberate.
"""

from __future__ import annotations

import datetime as dt
import logging

from .. import db, notify
from . import Source

log = logging.getLogger("collector.solar")

SWPC = "https://services.swpc.noaa.gov"
KC2G = "https://prop.kc2g.com"

FLUX_DDL = """
CREATE TABLE IF NOT EXISTS swpc_flux (
    time_tag   timestamptz PRIMARY KEY,
    flux       numeric(8,2) NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now()
);
"""

KP_DDL = """
CREATE TABLE IF NOT EXISTS swpc_kp (
    time_tag      timestamptz PRIMARY KEY,
    kp            numeric(5,2) NOT NULL,
    a_running     integer,
    station_count integer,
    fetched_at    timestamptz NOT NULL DEFAULT now()
);
"""

DAILY_DDL = """
CREATE TABLE IF NOT EXISTS solar_daily (
    day             date PRIMARY KEY,
    flux_10cm       numeric(8,2),
    sunspot_number  integer,
    sunspot_area    integer,
    new_regions     integer,
    xray_c          integer,
    xray_m          integer,
    xray_x          integer,
    fetched_at      timestamptz NOT NULL DEFAULT now()
);
"""

ESSN_DDL = """
CREATE TABLE IF NOT EXISTS essn (
    time       timestamptz NOT NULL,
    span       text        NOT NULL,
    ssn        numeric(8,2),
    sfi        numeric(8,2),
    fetched_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (time, span)
);
CREATE INDEX IF NOT EXISTS essn_time ON essn (time DESC);
"""
# The smoothing span column is `span`, not `window`: WINDOW is a reserved word
# in postgres (the SQL:2003 windowing clause) and an unquoted column of that
# name is a syntax error at CREATE TABLE time.


def _num(v):
    """SWPC mixes ints, floats, scientific-notation strings, nulls and "Unk"."""
    if v is None:
        return None
    if isinstance(v, (int, float)):
        return float(v)
    s = str(v).strip()
    if not s or s.lower() in ("unk", "unknown", "null", "none", "*"):
        return None
    try:
        return float(s)
    except ValueError:
        return None


def _int(v):
    """DSD uses -999 as its missing-value sentinel, which is not a real count."""
    f = _num(v)
    if f is None or f <= -900:
        return None
    return int(f)


async def _get_json(session, url):
    async with session.get(url) as r:
        r.raise_for_status()
        return await r.json(content_type=None)


async def _get_text(session, url):
    async with session.get(url) as r:
        r.raise_for_status()
        return await r.text()


# --------------------------------------------------------------- 10.7cm flux


async def fetch_flux(session) -> int:
    data = await _get_json(session, f"{SWPC}/products/summary/10cm-flux.json")
    rows = [
        (d["time_tag"], _num(d.get("flux")))
        for d in data
        if d.get("time_tag") and _num(d.get("flux")) is not None
    ]
    return await db.executemany(
        """INSERT INTO swpc_flux (time_tag, flux) VALUES (%s, %s)
           ON CONFLICT (time_tag) DO UPDATE SET flux = EXCLUDED.flux""",
        rows,
    )


# ------------------------------------------------------------- planetary Kp


async def fetch_kp(session) -> int:
    data = await _get_json(session, f"{SWPC}/products/noaa-planetary-k-index.json")
    rows = []
    for d in data:
        kp = _num(d.get("Kp"))
        if d.get("time_tag") is None or kp is None:
            continue
        rows.append((d["time_tag"], kp, d.get("a_running"), d.get("station_count")))
    n = await db.executemany(
        """INSERT INTO swpc_kp (time_tag, kp, a_running, station_count)
           VALUES (%s, %s, %s, %s)
           ON CONFLICT (time_tag) DO UPDATE
             SET kp = EXCLUDED.kp,
                 a_running = EXCLUDED.a_running,
                 station_count = EXCLUDED.station_count""",
        rows,
    )

    # Newest sample is LAST -- this feed is ascending by time.
    if rows:
        latest_kp = rows[-1][1]
        # Kp 5 is the G1 storm threshold: HF degradation on the high bands and
        # auroral absorption on polar paths. Below that it is just weather.
        if latest_kp >= 5:
            await notify.push(
                title=f"Geomagnetic storm: Kp {latest_kp:.1f}",
                message=(
                    f"Kp reached {latest_kp:.1f} at {rows[-1][0]} UTC (G1+). "
                    "Expect HF absorption at high latitudes and possible aurora."
                ),
                tags="zap",
                priority=4,
                dedupe_key=f"kp-{rows[-1][0]}",
            )
    return n


# ------------------------------------------------- daily sunspot number (DSD)


def parse_dsd(text: str) -> list[tuple]:
    """Parse the DSD fixed-width report into rows.

    Data lines start with a 4-digit year. Everything else is a ':' header or a
    '#' comment, including the column legend, which is why we key off the year
    rather than off a line number.
    """
    out = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line[0] in "#:":
            continue
        f = line.split()
        if len(f) < 12 or not (len(f[0]) == 4 and f[0].isdigit()):
            continue
        try:
            day = dt.date(int(f[0]), int(f[1]), int(f[2]))
        except ValueError:
            continue
        out.append((
            day,
            _num(f[3]),    # 10.7cm radio flux
            _int(f[4]),    # SESC sunspot number
            _int(f[5]),    # sunspot area, 10E-6 hemispheres
            _int(f[6]),    # new regions
            _int(f[9]),    # C-class flare count
            _int(f[10]),   # M-class
            _int(f[11]),   # X-class
        ))
    return out


async def fetch_daily(session) -> int:
    text = await _get_text(session, f"{SWPC}/text/daily-solar-indices.txt")
    rows = parse_dsd(text)
    if not rows:
        raise RuntimeError("DSD report parsed to zero rows -- upstream format changed?")
    return await db.executemany(
        """INSERT INTO solar_daily
             (day, flux_10cm, sunspot_number, sunspot_area, new_regions,
              xray_c, xray_m, xray_x)
           VALUES (%s,%s,%s,%s,%s,%s,%s,%s)
           ON CONFLICT (day) DO UPDATE
             SET flux_10cm = EXCLUDED.flux_10cm,
                 sunspot_number = EXCLUDED.sunspot_number,
                 sunspot_area = EXCLUDED.sunspot_area,
                 new_regions = EXCLUDED.new_regions,
                 xray_c = EXCLUDED.xray_c,
                 xray_m = EXCLUDED.xray_m,
                 xray_x = EXCLUDED.xray_x""",
        rows,
    )


# --------------------------------------------------- effective SSN (kc2g)


async def fetch_essn(session) -> int:
    data = await _get_json(session, f"{KC2G}/api/essn.json")
    rows = []
    for span in ("6h", "24h"):
        for d in data.get(span) or []:
            ts = d.get("time")
            if ts is None:
                continue
            when = dt.datetime.fromtimestamp(int(ts), dt.timezone.utc)
            rows.append((when, span, _num(d.get("ssn")), _num(d.get("sfi"))))
    if not rows:
        raise RuntimeError("essn.json had no 6h/24h samples -- upstream shape changed?")
    return await db.executemany(
        """INSERT INTO essn (time, span, ssn, sfi) VALUES (%s,%s,%s,%s)
           ON CONFLICT (time, span) DO UPDATE
             SET ssn = EXCLUDED.ssn, sfi = EXCLUDED.sfi""",
        rows,
    )


SOURCES = [
    Source(
        name="swpc_flux",
        interval=3600,
        ddl=FLUX_DDL,
        fetch=fetch_flux,
        description="10.7cm solar flux (current value)",
        tags=["ham", "propagation", "solar"],
    ),
    Source(
        name="swpc_kp",
        interval=900,
        ddl=KP_DDL,
        fetch=fetch_kp,
        description="Planetary K index (geomagnetic activity)",
        tags=["ham", "propagation", "solar"],
    ),
    Source(
        name="solar_daily",
        interval=3600,
        ddl=DAILY_DDL,
        fetch=fetch_daily,
        description="Daily sunspot number + flux, 30 day history (SWPC DSD)",
        tags=["ham", "propagation", "solar"],
    ),
    Source(
        name="essn",
        interval=900,
        ddl=ESSN_DDL,
        fetch=fetch_essn,
        description="Effective SSN/SFI from ionosonde assimilation (kc2g)",
        tags=["ham", "propagation", "solar"],
    ),
]
