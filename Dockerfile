FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/openfox-server ./cmd/openfox-server
COPY internal ./internal
ARG OPENFOX_VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-X main.version=${OPENFOX_VERSION}" -o /out/openfox-server ./cmd/openfox-server \
    && install -d -m 1777 /out/tmp \
    && install -d -o 65532 -g 65532 /out/storage

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/tmp /tmp
COPY --from=build --chown=65532:65532 /out/storage /var/lib/openfox
COPY --from=build /out/openfox-server /openfox-server
WORKDIR /app
COPY db/migrations/*.sql ./db/migrations/
ENV OPENFOX_ENV=production OPENFOX_SERVER_LISTEN=0.0.0.0:8080 OPENFOX_STORAGE_ROOT=/var/lib/openfox
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/openfox-server"]
CMD ["serve"]
