# Servidor MCP de trámites por HTTP. Mismo esquema que dobled-bff: compila en golang:alpine
# y corre en alpine. Dokploy termina el HTTPS (tramites.dropia.uy) y reenvía al puerto 8080.
#
#   docker build -t mcp-tramites-gub-uy .
#   docker run -p 8080:8080 -v tramites-cache:/data mcp-tramites-gub-uy
#   -> endpoint MCP: http://localhost:8080/mcp

FROM golang:1.27-alpine AS builder

RUN apk add --no-cache tzdata ca-certificates

# TARGETARCH lo pone docker buildx: amd64 en el servidor, arm64 en una Mac.
ARG TARGETARCH
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=${TARGETARCH}

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN go build -trimpath -ldflags="-s -w" -o /mcp-tramites-gub-uy .

FROM alpine:3

RUN apk add --no-cache tzdata ca-certificates && \
    addgroup -S -g 1000 app && \
    adduser -S -u 1000 -G app app && \
    mkdir -p /data && chown app:app /data

COPY --from=builder /mcp-tramites-gub-uy /mcp-tramites-gub-uy

# La caché de los catálogos (unos 12 MB) va al volumen: un redeploy arranca sin descargar.
ENV TZ="America/Montevideo" \
    MCP_HTTP_ADDR=":8080" \
    TRAMITES_CACHE_DIR="/data"
VOLUME ["/data"]

USER app

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/health || exit 1

CMD ["/mcp-tramites-gub-uy"]
