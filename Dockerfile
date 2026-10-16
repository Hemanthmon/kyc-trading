FROM golang:1.27-alpine AS build
WORKDIR /src

# Download dependencies first so this layer is cached between code changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pricing-engine .

# ---- run stage: small image with only the binary ----
FROM alpine:3.22
# ca-certificates: needed for the wss:// TLS connection to the feed
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app

COPY --from=build /out/pricing-engine /usr/local/bin/pricing-engine

EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s \
  CMD wget -qO- http://localhost:8080/health >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/pricing-engine"]
