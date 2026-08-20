FROM golang:1.26.7-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/dlp ./cmd/dlp

FROM alpine:3.20
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/dlp /app/dlp
USER app
EXPOSE 8084
ENTRYPOINT ["/app/dlp"]
