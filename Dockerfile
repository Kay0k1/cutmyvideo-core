FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /cutmy ./cmd/cutmy

FROM node:25-bookworm-slim@sha256:81db02c4b671288a03915da9534dbd54f96d0e7c24d80ccc54f5b36b2e684370
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg python3 curl \
    && rm -rf /var/lib/apt/lists/* \
    && curl --fail --show-error --location --proto '=https' --tlsv1.2 https://github.com/yt-dlp/yt-dlp/releases/download/2026.08.19/yt-dlp -o /usr/local/bin/yt-dlp \
    && echo '1fa6733c37ea6fb51c99ad8fe785e7b7e5f3246c9b980230329d4fb72ed8d4d6  /usr/local/bin/yt-dlp' | sha256sum --check \
    && chmod 755 /usr/local/bin/yt-dlp \
    && mkdir -p /data && chown node:node /data
COPY --from=build /cutmy /usr/local/bin/cutmy
USER node
ENV DATA_DIR=/data LISTEN_ADDR=:8080 FFMPEG_THREADS=2 FFMPEG_PROFILE=fast GOMEMLIMIT=128MiB
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cutmy"]
CMD ["server"]
