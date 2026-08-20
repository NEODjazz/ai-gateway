FROM golang:1.26.7-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/av ./cmd/av

FROM alpine:3.20
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/av /app/av
USER app
EXPOSE 8085
ENTRYPOINT ["/app/av"]
