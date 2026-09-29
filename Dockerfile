FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/discovery2api ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
WORKDIR /app
COPY --from=builder /out/discovery2api /usr/local/bin/discovery2api
COPY config.example.yaml /app/config.example.yaml
# app 用户要能创建用量目录；配置文件由挂载卷提供，这里只保证目录可写。
RUN mkdir -p /app/state && chown -R app:app /app
USER app
EXPOSE 8319
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://127.0.0.1:8319/healthz || exit 1
ENTRYPOINT ["discovery2api"]
CMD ["-c", "/app/config.yaml"]
