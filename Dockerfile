# Build a static releasejo binary and ship it on a tiny base. The image doubles
# as the Forgejo Action runtime (see action.yml).
FROM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src
# Stdlib-only: no `go mod download` step needed (no third-party deps).
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/releasejo ./cmd/releasejo

FROM docker.io/library/alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/releasejo /usr/local/bin/releasejo
COPY action/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
