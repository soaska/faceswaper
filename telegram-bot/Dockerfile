FROM golang:1.26.5-alpine AS builder

# Build arguments for version info
ARG GIT_COMMIT=unknown

WORKDIR /build
COPY shared/go.mod shared/go.sum ./shared/
COPY telegram-bot/go.mod telegram-bot/go.sum ./telegram-bot/
WORKDIR /build/telegram-bot
RUN go mod download
WORKDIR /build
COPY shared ./shared
COPY telegram-bot ./telegram-bot
WORKDIR /build/telegram-bot
RUN CGO_ENABLED=0 go build -ldflags "-X main.GitCommit=${GIT_COMMIT}" -o ./main

FROM alpine:3.22
LABEL org.opencontainers.image.source=https://github.com/soaska/faceswaper
LABEL org.opencontainers.image.description="telegram bot container image. part of the project"
LABEL org.opencontainers.image.licenses=MPL-2.0
WORKDIR /app
RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /build/telegram-bot/main ./main
ENTRYPOINT ["./main"]
