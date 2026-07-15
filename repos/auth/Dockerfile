FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/auth ./cmd/auth

FROM alpine:3.20
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/auth /app/auth
USER app
EXPOSE 8082
ENTRYPOINT ["/app/auth"]
