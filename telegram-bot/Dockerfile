FROM golang:latest as builder
WORKDIR /build
COPY ./ ./
RUN go mod download
RUN CGO_ENABLED=0 go build -o ./main


FROM alpine:latest
WORKDIR /app
COPY --from=builder /build/main ./main
RUN apk --no-cache add ca-certificates tzdata
ENTRYPOINT ["./main"]
