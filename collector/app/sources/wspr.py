"""WSPR Live -- the public ClickHouse mirror of WSPRnet, at db1.wspr.live.

One of the best free propagation datasets in amateur radio: every WSPR reception
report, queryable with SQL over HTTP. This is *measured* propagation -- who
actually heard whom, on which band, right now -- as opposed to a prediction.

Verified live: GET https://db1.wspr.live/?query=<SQL>%20FORMAT%20JSONEachRow
returns newline delimited JSON. The wspr.rx table has (among others):
  time, band, tx_sign, tx_loc, rx_sign, rx_loc, distance, azimuth, frequency,
  power, snr, drift, version, code

`band` is an integer MHz bucket (0=LF/MF, 7=40m, 14=20m, 144=2m, ...) and -1
appears for out-of-band reports.

RATE LIMIT: wspr.live is a volunteer service and asks that you do not hammer it.
This source runs at a 10 minute interval with a narrow time window. Do not
shorten it.
"""

from __future__ import annotations

import json
import logging

from .. import db
from . import Source

log = logging.getLogger("collector.wspr")

URL = "https://db1.wspr.live/"

BAND_DDL = """
CREATE TABLE IF NOT EXISTS wspr_band_activity (
    bucket     timestamptz NOT NULL,
    band       integer     NOT NULL,
    spots      bigint      NOT NULL,
    tx_count   bigint,
    rx_count   bigint,
    avg_snr    numeric(8,2),
    max_km     numeric(12,1),
    fetched_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (bucket, band)
);
CREATE INDEX IF NOT EXISTS wspr_band_activity_bucket ON wspr_band_activity (bucket DESC);
"""


async def _query(session, sql: str) -> list[dict]:
    params = {"query": f"{sql} FORMAT JSONEachRow"}
    async with session.get(URL, params=params) as r:
        text = await r.text()
        if r.status != 200:
            raise RuntimeError(f"wspr.live HTTP {r.status}: {text[:200]}")
        out = []
        for line in text.splitlines():
            line = line.strip()
            if line:
                out.append(json.loads(line))
        return out


# A full day is 144 ten-minute buckets. Below this many we assume the history
# has a hole worth filling -- a fresh install, or the collector having been
# down -- and pull the whole day in one aggregate instead of the usual 20
# minutes. The waterfall view in propscope is unreadable without a day of
# history, and waiting 24h to populate it is not a real option.
BACKFILL_BELOW = 100
BACKFILL_HOURS = 24


async def fetch_band_activity(session) -> int:
    """Global band-by-band activity in 10-minute buckets.

    This is the propagation weathervane: which bands are actually open right
    now, worldwide, measured rather than predicted.

    Normally this asks for the last 20 minutes -- two buckets, so the most
    recent one is re-fetched once after it closes. A bucket queried while it is
    still filling is frozen at a partial count, which is what the ON CONFLICT
    UPDATE below exists to correct.

    If the last day is mostly missing it asks for 24 hours instead. That is a
    heavier query against a volunteer service, so it is deliberately gated on
    the hole being real and it logs when it fires. It aggregates server-side and
    returns ~2400 rows, not millions of spots.
    """
    have = await db.fetchone(
        "SELECT count(DISTINCT bucket) FROM wspr_band_activity "
        "WHERE bucket > now() - INTERVAL '24 hours'"
    )
    buckets = (have[0] if have else 0) or 0

    if buckets < BACKFILL_BELOW:
        window = f"INTERVAL {BACKFILL_HOURS} HOUR"
        log.info("wspr: only %d of 144 buckets in the last 24h -- backfilling %dh",
                 buckets, BACKFILL_HOURS)
    else:
        window = "INTERVAL 20 MINUTE"

    sql = (
        "SELECT toStartOfTenMinutes(time) AS bucket, band, count() AS spots, "
        "       uniqExact(tx_sign) AS tx_count, uniqExact(rx_sign) AS rx_count, "
        "       avg(snr) AS avg_snr, max(distance) AS max_km "
        "FROM wspr.rx "
        f"WHERE time >= toStartOfTenMinutes(now() - {window}) "
        "GROUP BY bucket, band ORDER BY bucket, band"
    )
    data = await _query(session, sql)
    rows = [
        (d["bucket"], d["band"], d["spots"], d.get("tx_count"), d.get("rx_count"),
         d.get("avg_snr"), d.get("max_km"))
        for d in data if d.get("bucket") and d.get("band") is not None
    ]
    return await db.executemany(
        """INSERT INTO wspr_band_activity
             (bucket, band, spots, tx_count, rx_count, avg_snr, max_km)
           VALUES (%s,%s,%s,%s,%s,%s,%s)
           ON CONFLICT (bucket, band) DO UPDATE
             SET spots = EXCLUDED.spots,
                 tx_count = EXCLUDED.tx_count,
                 rx_count = EXCLUDED.rx_count,
                 avg_snr = EXCLUDED.avg_snr,
                 max_km = EXCLUDED.max_km""",
        rows,
    )


SOURCES = [
    Source(
        name="wspr_band_activity",
        interval=600,
        ddl=BAND_DDL,
        fetch=fetch_band_activity,
        description="Worldwide WSPR spots per band -- measured propagation",
        tags=["ham", "propagation", "wspr"],
    ),
]
