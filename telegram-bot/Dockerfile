FROM golang:latest AS builder

# Build arguments for version info
ARG GIT_COMMIT=unknown
ARG GIT_MESSAGE=unknown

WORKDIR /build
COPY ./ ./
RUN go mod download
RUN CGO_ENABLED=0 go build -ldflags "-X main.GitCommit=${GIT_COMMIT} -X 'main.GitMessage=${GIT_MESSAGE}'" -o ./main

FROM alpine:latest
LABEL org.opencontainers.image.source=https://github.com/soaska/faceswaper
LABEL org.opencontainers.image.description="telegram bot container image. part of the project"
LABEL org.opencontainers.image.licenses=MPL-2.0
WORKDIR /app
RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /build/main ./main
ENTRYPOINT ["./main"]
