FROM golang:1.25-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/fb_comment .

FROM mcr.microsoft.com/playwright:v1.55.0-jammy

ENV APP_PORT=8080 \
    SCRAPER_HEADLESS=true \
    GIN_MODE=release

WORKDIR /home/pwuser/app

COPY --from=builder /out/fb_comment ./fb_comment
COPY --from=builder /src/view ./view

EXPOSE 8080

USER pwuser

CMD ["/home/pwuser/app/fb_comment"]
