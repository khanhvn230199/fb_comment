FROM golang:1.25-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
RUN go install github.com/mxschmitt/playwright-go/cmd/playwright@v0.6100.0

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/fb_comment .

FROM mcr.microsoft.com/playwright:v1.61.1-jammy

ENV APP_PORT=8080 \
    SCRAPER_HEADLESS=true \
    GIN_MODE=release \
    HOME=/home/pwuser

WORKDIR /home/pwuser/app

COPY --from=builder /out/fb_comment ./fb_comment
COPY --from=builder /src/view ./view
COPY --from=builder /go/bin/playwright /usr/local/bin/playwright

USER pwuser
RUN playwright install chromium

HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=10 CMD node -e "fetch('http://127.0.0.1:8080/healthz').then(r=>process.exit(r.ok ? 0 : 1)).catch(() => process.exit(1))"

EXPOSE 8080

CMD ["/home/pwuser/app/fb_comment"]
