FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/billing ./cmd/billing

FROM alpine:3.20
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/billing /app/billing
USER app
EXPOSE 8083
ENTRYPOINT ["/app/billing"]
