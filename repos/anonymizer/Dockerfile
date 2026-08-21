FROM golang:1.26.7-alpine AS builder

WORKDIR /src
COPY go.* ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/anonymizer ./cmd/anonymizer

FROM alpine:3.20
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/anonymizer /app/anonymizer
USER app
EXPOSE 8081
ENTRYPOINT ["/app/anonymizer"]
