FROM node:26.3.1-alpine AS ui-builder

WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci --ignore-scripts
COPY ui/index.html ui/tsconfig.json ui/tsconfig.app.json ui/tsconfig.node.json ui/vite.config.ts ./
COPY ui/src ./src
RUN npm rebuild esbuild && npm run build

FROM golang:1.26.7-alpine AS builder

WORKDIR /src
COPY go.* ./
RUN go mod download
COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
COPY --from=ui-builder /src/internal/gateway/adminui ./internal/gateway/adminui
RUN go build -o /out/gateway ./cmd/gateway

FROM alpine:3.20
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/gateway /app/gateway
USER app
EXPOSE 8080
ENTRYPOINT ["/app/gateway"]
