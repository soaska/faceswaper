FROM golang:1.26.5-alpine AS builder

# Build arguments for version info
ARG GIT_COMMIT=unknown

WORKDIR /build
COPY shared/go.mod ./shared/go.mod
COPY job-manager/go.mod job-manager/go.sum ./job-manager/
WORKDIR /build/job-manager
RUN go mod download
WORKDIR /build
COPY shared ./shared
COPY job-manager ./job-manager
WORKDIR /build/job-manager
RUN CGO_ENABLED=0 go build -ldflags "-X main.GitCommit=${GIT_COMMIT}" -o ./main

FROM alpine:3.22
LABEL org.opencontainers.image.source=https://github.com/soaska/faceswaper
LABEL org.opencontainers.image.description="job-manager container image"
LABEL org.opencontainers.image.licenses=MPL-2.0
WORKDIR /app
RUN apk --no-cache add ca-certificates tzdata ffmpeg
COPY --from=builder /build/job-manager/main ./main
ENTRYPOINT ["./main"]
