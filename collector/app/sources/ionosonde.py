"""Measured ionospheric state: vertical soundings, plus local HF absorption.

Two feeds, both verified live 2026-08-03.

  prop.kc2g.com/api/stations.json
      A JSON list of the MOST RECENT sounding from every ionosonde kc2g knows
      about, ~101 stations. Verified shape:
        {"id":22008538,"station":{"code":"AU930","name":"Austin, TX, USA",
         "latitude":"30.4","longitude":"262.3"},
         "time":"2026-03-19T22:10:05","fof2":8.6,"mufd":28.827,"hmf2":241.8,
         "md":"3.352","tec":15.837,"cs":100.0,"foe":3.32,"foes":3.6, ...}
      Longitude is 0..360 EAST, not -180..180. `md` arrives as a STRING.

      READ THIS BEFORE TRUSTING A STATION: the list is "latest per station",
      never "currently live". Many entries are years stale -- there are rows
      from 2015 in there. The Austin TX sounder (AU930) is one of them: its last
      sounding was 2026-03-19 and it has been silent since, which is why this
      module ranks stations by distance from the QTH but filters HARD on
      freshness first. A stale foF2 presented as current is worse than no foF2.
      (GIRO's own DIDBGetValues servlet at lgdc.uml.edu now returns 404 at every
      parameter format, so kc2g is the practical route to this data.)

  services.swpc.noaa.gov/text/drap_global_frequencies.txt
      D-Region Absorption Prediction, as a fixed-width lat/lon grid of the
      Highest Affected Frequency in MHz:
        # Product Valid At : 2026-08-03 02:51 UTC
        #
              -178 -174 -170 ... 178
        ------------------------------
         89 |  1.3  1.3  1.3 ...
      Latitudes 89..-89 step 2, longitudes -178..178 step 4. Unlike the
      soundings this IS available for Austin -- it is a global model sampled at
      a point -- and it answers the other half of "what is open": D-region
      absorption sets the LOWER edge (the LUF), while foF2/MUF set the upper.
"""

from __future__ import annotations

import datetime as dt
import logging
import math
import os

from .. import db
from . import Source

log = logging.getLogger("collector.ionosonde")

KC2G = "https://prop.kc2g.com"
SWPC = "https://services.swpc.noaa.gov"

# Home QTH. Austin, TX unless overridden. Distances and the D-RAP sample point
# are both taken from here.
QTH_LAT = float(os.environ.get("QTH_LAT", "30.2672"))
QTH_LON = float(os.environ.get("QTH_LON", "-97.7431"))

# How old a sounding may be and still be worth storing.
#
# 24h, not the 3h you would expect, and the reason is measured rather than
# guessed. Observed distribution across all 101 stations on 2026-08-03:
#     <1h: 2      1-6h: 0      6-24h: 28      older: 71
# The feed is BIMODAL. A couple of sounders report continuously; the rest of
# the live network lands in kc2g in a batch around 20:50 UTC and then nothing
# moves for hours. A 3h window therefore captures ~2 stations for most of the
# day and looks like an outage when it is really just the upstream's cadence.
#
# Storing up to 24h is only honest if the AGE travels with the value, so
# ionosonde_obs keeps the sounding's own timestamp and every consumer is
# expected to show it. A 9-hour-old foF2 is useful; a 9-hour-old foF2 presented
# as "now" is a lie.
FRESH_S = int(os.environ.get("IONOSONDE_FRESH_S", str(24 * 3600)))

STATION_DDL = """
CREATE TABLE IF NOT EXISTS ionosonde_station (
    code        text PRIMARY KEY,
    name        text,
    lat         numeric(8,3),
    lon         numeric(8,3),
    km_from_qth numeric(10,1),
    last_seen   timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ionosonde_station_dist ON ionosonde_station (km_from_qth);
"""

OBS_DDL = """
CREATE TABLE IF NOT EXISTS ionosonde_obs (
    station    text        NOT NULL,
    time       timestamptz NOT NULL,
    fof2       numeric(8,3),
    mufd       numeric(8,3),
    hmf2       numeric(8,2),
    foe        numeric(8,3),
    foes       numeric(8,3),
    tec        numeric(8,2),
    md         numeric(8,3),
    cs         numeric(5,1),
    fetched_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (station, time)
);
CREATE INDEX IF NOT EXISTS ionosonde_obs_time ON ionosonde_obs (time DESC);
"""

DRAP_DDL = """
CREATE TABLE IF NOT EXISTS drap_local (
    valid_at   timestamptz PRIMARY KEY,
    lat        numeric(8,3) NOT NULL,
    lon        numeric(8,3) NOT NULL,
    haf_mhz    numeric(8,2),
    global_max numeric(8,2),
    fetched_at timestamptz NOT NULL DEFAULT now()
);
"""


def _num(v):
    if v is None:
        return None
    if isinstance(v, (int, float)):
        return float(v)
    s = str(v).strip()
    if not s or s.lower() in ("unk", "unknown", "null", "none", "*", "-"):
        return None
    try:
        return float(s)
    except ValueError:
        return None


def haversine_km(lat1, lon1, lat2, lon2) -> float:
    r = 6371.0088
    p1, p2 = math.radians(lat1), math.radians(lat2)
    dp = math.radians(lat2 - lat1)
    dl = math.radians(lon2 - lon1)
    a = math.sin(dp / 2) ** 2 + math.cos(p1) * math.cos(p2) * math.sin(dl / 2) ** 2
    return 2 * r * math.asin(math.sqrt(a))


def _norm_lon(lon: float) -> float:
    """kc2g publishes 0..360 east; everything else here wants -180..180."""
    lon = ((lon + 180.0) % 360.0) - 180.0
    return lon


async def _get_json(session, url):
    async with session.get(url) as r:
        r.raise_for_status()
        return await r.json(content_type=None)


async def _get_text(session, url):
    async with session.get(url) as r:
        r.raise_for_status()
        return await r.text()


# ------------------------------------------------------------- ionosondes


async def fetch_soundings(session) -> int:
    data = await _get_json(session, f"{KC2G}/api/stations.json")
    if not isinstance(data, list) or not data:
        raise RuntimeError("kc2g stations.json was not a non-empty list")

    now = dt.datetime.now(dt.timezone.utc)
    stations, obs = [], []
    stale = 0

    for d in data:
        st = d.get("station") or {}
        code = (st.get("code") or "").strip()
        raw_time = d.get("time")
        if not code or not raw_time:
            continue

        # kc2g timestamps are naive UTC.
        try:
            when = dt.datetime.fromisoformat(raw_time)
        except ValueError:
            continue
        if when.tzinfo is None:
            when = when.replace(tzinfo=dt.timezone.utc)

        lat, lon = _num(st.get("latitude")), _num(st.get("longitude"))
        km = None
        if lat is not None and lon is not None:
            km = round(haversine_km(QTH_LAT, QTH_LON, lat, _norm_lon(lon)), 1)
            lon = _norm_lon(lon)

        # The station registry records everything, including long-dead sites --
        # last_seen is exactly how you tell a live sounder from a dead one.
        stations.append((code, st.get("name") or code, lat, lon, km, when))

        if (now - when).total_seconds() > FRESH_S:
            stale += 1
            continue

        obs.append((
            code, when, _num(d.get("fof2")), _num(d.get("mufd")),
            _num(d.get("hmf2")), _num(d.get("foe")), _num(d.get("foes")),
            _num(d.get("tec")), _num(d.get("md")), _num(d.get("cs")),
        ))

    await db.executemany(
        """INSERT INTO ionosonde_station (code, name, lat, lon, km_from_qth, last_seen)
           VALUES (%s,%s,%s,%s,%s,%s)
           ON CONFLICT (code) DO UPDATE
             SET name = EXCLUDED.name,
                 lat = EXCLUDED.lat,
                 lon = EXCLUDED.lon,
                 km_from_qth = EXCLUDED.km_from_qth,
                 last_seen = GREATEST(ionosonde_station.last_seen, EXCLUDED.last_seen),
                 updated_at = now()""",
        stations,
    )

    n = await db.executemany(
        """INSERT INTO ionosonde_obs
             (station, time, fof2, mufd, hmf2, foe, foes, tec, md, cs)
           VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)
           ON CONFLICT (station, time) DO NOTHING""",
        obs,
    )
    log.info("ionosonde: %d station(s), %d fresh, %d stale (>%ds)",
             len(stations), len(obs), stale, FRESH_S)
    return n


# ------------------------------------------------------------------- D-RAP


def parse_drap(text: str, lat: float, lon: float) -> tuple:
    """Return (valid_at, haf_at_point, global_max) from the DRAP grid."""
    valid_at = None
    lons: list[float] = []
    best = None          # (|dlat|, haf) for the requested latitude row
    gmax = None

    for raw in text.splitlines():
        line = raw.rstrip()
        if not line:
            continue

        if line.startswith("#"):
            if "Product Valid At" in line:
                # "# Product Valid At : 2026-08-03 02:51 UTC"
                stamp = line.split(":", 1)[1].strip()
                stamp = stamp.replace("Product Valid At", "").lstrip(": ").strip()
                stamp = stamp.replace(" UTC", "")
                try:
                    valid_at = dt.datetime.strptime(
                        stamp, "%Y-%m-%d %H:%M").replace(tzinfo=dt.timezone.utc)
                except ValueError:
                    pass
            continue

        # The longitude header is the only non-comment line with no '|'.
        if "|" not in line:
            parts = line.replace("-", " -").split()
            cand = []
            for p in parts:
                v = _num(p)
                if v is None:
                    cand = []
                    break
                cand.append(v)
            if len(cand) > 10:
                lons = cand
            continue

        left, _, right = line.partition("|")
        rlat = _num(left)
        if rlat is None or not lons:
            continue
        vals = [_num(x) for x in right.split()]
        for v in vals:
            if v is not None and (gmax is None or v > gmax):
                gmax = v
        if len(vals) != len(lons):
            continue

        # Nearest grid column to the requested longitude.
        j = min(range(len(lons)), key=lambda i: abs(lons[i] - lon))
        d = abs(rlat - lat)
        if vals[j] is not None and (best is None or d < best[0]):
            best = (d, vals[j])

    return valid_at, (best[1] if best else None), gmax


async def fetch_drap(session) -> int:
    text = await _get_text(session, f"{SWPC}/text/drap_global_frequencies.txt")
    valid_at, haf, gmax = parse_drap(text, QTH_LAT, QTH_LON)
    if valid_at is None:
        raise RuntimeError("DRAP: no 'Product Valid At' header -- format changed?")
    if haf is None:
        raise RuntimeError("DRAP: grid parsed but no value at QTH -- format changed?")
    return await db.executemany(
        """INSERT INTO drap_local (valid_at, lat, lon, haf_mhz, global_max)
           VALUES (%s,%s,%s,%s,%s)
           ON CONFLICT (valid_at) DO UPDATE
             SET haf_mhz = EXCLUDED.haf_mhz, global_max = EXCLUDED.global_max""",
        [(valid_at, QTH_LAT, QTH_LON, haf, gmax)],
    )


SOURCES = [
    Source(
        name="ionosonde",
        interval=900,
        ddl=STATION_DDL + OBS_DDL,
        fetch=fetch_soundings,
        description="Ionosonde soundings (foF2/MUF), ranked by distance from QTH",
        tags=["ham", "propagation", "ionosphere"],
    ),
    Source(
        name="drap",
        interval=900,
        ddl=DRAP_DDL,
        fetch=fetch_drap,
        description="D-region absorption at the QTH -- the LUF side of band openness",
        tags=["ham", "propagation", "ionosphere"],
    ),
]
