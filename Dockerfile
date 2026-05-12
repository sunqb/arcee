# ---- build stage ----
FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /arcee .

# ---- final stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /arcee /app/arcee

# tokens 持久化目录
VOLUME ["/app/tokens"]

EXPOSE 8787
ENTRYPOINT ["/app/arcee"]
