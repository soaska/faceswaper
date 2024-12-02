FROM golang:latest as builder
WORKDIR /build
COPY ./ ./
RUN go mod download
RUN CGO_ENABLED=0 go build -o ./main

FROM alpine:latest
LABEL org.opencontainers.image.source=https://github.com/soaska/faceswaper
LABEL org.opencontainers.image.description="telegram bot container image. part of the project"
LABEL org.opencontainers.image.licenses=MPL-2.0
WORKDIR /app
COPY --from=builder /build/main ./main
RUN apk --no-cache add ca-certificates tzdata
ENTRYPOINT ["./main"]
