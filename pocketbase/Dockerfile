FROM alpine:3.22
LABEL org.opencontainers.image.source=https://github.com/soaska/faceswaper
LABEL org.opencontainers.image.description="PocketBase container image with project-specific migrations"
LABEL org.opencontainers.image.licenses=MPL-2.0

ARG PB_VERSION=0.22.47
ARG PB_SHA256=a2099ec2b79842549656d696d6684cc49f7cc3dca83a4fe5b18c2c2fd817b172

RUN apk add --no-cache \
    unzip \
    ca-certificates \
    curl

# download and unzip PocketBase
RUN wget -q "https://github.com/pocketbase/pocketbase/releases/download/v${PB_VERSION}/pocketbase_${PB_VERSION}_linux_amd64.zip" -O /tmp/pb.zip \
    && echo "${PB_SHA256}  /tmp/pb.zip" | sha256sum -c - \
    && unzip /tmp/pb.zip -d /pb/ \
    && rm /tmp/pb.zip

COPY ./collections/* /pb/pb_migrations/

# Project routes provide atomic queue claims and user balance updates.
COPY ./pb_hooks /pb/pb_hooks

EXPOSE 8080

# start PocketBase
CMD ["/pb/pocketbase", "serve", "--http=0.0.0.0:8080"]
