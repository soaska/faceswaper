FROM alpine:latest
LABEL org.opencontainers.image.source=https://github.com/soaska/faceswaper
LABEL org.opencontainers.image.description="PocketBase container image with project-specific migrations"
LABEL org.opencontainers.image.licenses=MPL-2.0

ARG PB_VERSION=0.22.27

RUN apk add --no-cache \
    unzip \
    ca-certificates

# download and unzip PocketBase
ADD https://github.com/pocketbase/pocketbase/releases/download/v${PB_VERSION}/pocketbase_${PB_VERSION}_linux_amd64.zip /tmp/pb.zip
RUN unzip /tmp/pb.zip -d /pb/

# uncomment to copy the local pb_migrations dir into the image
COPY ./collections/* /pb/pb_migrations/

# uncomment to copy the local pb_hooks dir into the image
# COPY ./pb_hooks /pb/pb_hooks

EXPOSE 8080

# start PocketBase
CMD ["/pb/pocketbase", "serve", "--http=0.0.0.0:8080"]