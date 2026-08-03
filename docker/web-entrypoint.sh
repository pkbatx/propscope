#!/bin/sh
# Regenerate the page on a timer, serve the directory with busybox httpd.
set -eu

: "${REFRESH:=300}"   # seconds between regenerations, also the page's meta-refresh
: "${WIDTH:=116}"     # desktop render width
: "${NARROW:=96}"     # phone render width; below 96 the two-column layout cannot fit
: "${PORT:=8080}"

gen() {
    # Write to a temp file and rename. rename(2) is atomic within a filesystem,
    # so a request that lands mid-regeneration gets the previous complete page
    # rather than half a document.
    if propscope -html -width "$WIDTH" -narrow "$NARROW" -refresh "$REFRESH" \
        > /srv/.index.new 2>/tmp/gen.err; then
        mv /srv/.index.new /srv/index.html
    else
        echo "propscope-web: generate failed: $(head -c 400 /tmp/gen.err)" >&2
        rm -f /srv/.index.new
    fi
}

# Never exit on a failed first run -- postgres may still be starting, and the
# httpd below should come up anyway so the healthcheck has something to talk to.
gen || true
if [ ! -f /srv/index.html ]; then
    printf '<!doctype html><meta http-equiv=refresh content=10><body style="background:#0b0d12;color:#d8dee9;font-family:monospace;padding:2rem">propscope: waiting for first collection\n' \
        > /srv/index.html
fi

while :; do
    sleep "$REFRESH"
    gen || true
done &

exec darkhttpd /srv --port "$PORT" --no-listing --index index.html
