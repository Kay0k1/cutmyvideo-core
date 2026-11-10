FROM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN GOTOOLCHAIN=local go mod download
COPY . .
ARG VERSION=dev
ARG VCS_REF=unknown
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -p=2 -mod=readonly -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X main.version=${VERSION} -X main.commit=${VCS_REF} -X main.buildTime=${BUILD_TIME}" -o /cutmy ./cmd/cutmy

FROM node:22-bookworm-slim@sha256:43ac6c60b8f89723f746e8a92ce91abd5017e627ce1ddfe4238355d3a30b772c
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg python3 curl \
    && rm -rf /var/lib/apt/lists/* \
    && curl --fail --show-error --location --proto '=https' --tlsv1.2 https://github.com/yt-dlp/yt-dlp/releases/download/2026.08.19/yt-dlp -o /usr/local/bin/yt-dlp \
    && echo '1fa6733c37ea6fb51c99ad8fe785e7b7e5f3246c9b980230329d4fb72ed8d4d6  /usr/local/bin/yt-dlp' | sha256sum --check \
    && chmod 755 /usr/local/bin/yt-dlp \
    && mkdir -p /data && chown node:node /data && chmod 700 /data
COPY --from=build /cutmy /usr/local/bin/cutmy
ARG VERSION=dev
ARG VCS_REF=unknown
ARG BUILD_TIME=unknown
LABEL org.opencontainers.image.version=${VERSION} \
      org.opencontainers.image.revision=${VCS_REF} \
      org.opencontainers.image.created=${BUILD_TIME} \
      org.opencontainers.image.source="https://github.com/Kay0k1/cutmyvideo-core"
USER node
ENV DATA_DIR=/data LISTEN_ADDR=:8080 FFMPEG_THREADS=2 FFMPEG_PROFILE=fast GOMEMLIMIT=128MiB
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cutmy"]
CMD ["server"]
