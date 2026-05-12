# ── Stage 1: Build ───────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN apk add --no-cache git

WORKDIR /build

ENV GOPROXY=https://goproxy.cn,direct

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags="-w -s" -o arcee .

# ── Stage 2: Run ─────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /build/arcee .

USER nonroot:nonroot

# tokens 持久化目录
VOLUME ["/app/tokens"]

EXPOSE 8787
ENTRYPOINT ["/app/arcee"]
