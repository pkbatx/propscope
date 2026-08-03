# propscope -- the TUI.
#
# Two stages: build with the full toolchain, ship a ~15 MB image with just the
# static binary. CGO is off so the result runs on alpine, distroless or scratch
# without a libc to match.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies first, so editing the UI does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/propscope .

# ---------------------------------------------------------------------------
FROM alpine:3.21

# ncurses-terminfo gives the container entries for common TERM values. propscope
# forces its own colour profile (see ensureColor) so it does not depend on this,
# but a terminfo-aware pager or editor invoked alongside it would.
RUN apk add --no-cache ca-certificates ncurses-terminfo-base \
 && adduser -D -u 10001 propscope

COPY --from=build /out/propscope /usr/local/bin/propscope

USER 10001
ENTRYPOINT ["/usr/local/bin/propscope"]
