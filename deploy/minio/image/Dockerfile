# Academic build recipe. Known security findings remain; see provenance.json.
# Base digests were observed in this project's earlier successful worker build.
FROM golang:1.26@sha256:672c944cfd07599b5f1bb179869270cdc94c6254e41db4d431259693cb286b82 AS build-base
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

FROM build-base AS server-build
WORKDIR /src/minio
RUN git init . && git remote add origin https://github.com/minio/minio.git \
    && git fetch --depth=1 origin 9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = 9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a
RUN mkdir -p /out \
    && go build -mod=readonly -trimpath \
      -ldflags="$(MINIO_RELEASE=RELEASE go run buildscripts/gen-ldflags.go 2025-10-15T17-29-55Z)" \
      -o /out/minio . \
    && cp LICENSE /out/LICENSE

FROM build-base AS client-build
WORKDIR /src/mc
RUN git init . && git remote add origin https://github.com/minio/mc.git \
    && git fetch --depth=1 origin 7394ce0dd2a80935aded936b09fa12cbb3cb8096 \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = 7394ce0dd2a80935aded936b09fa12cbb3cb8096
RUN mkdir -p /out \
    && go build -mod=readonly -trimpath \
      -ldflags="$(MC_RELEASE=RELEASE go run buildscripts/gen-ldflags.go 2025-08-13T08-35-41Z)" \
      -o /out/mc . \
    && cp LICENSE /out/LICENSE

FROM alpine:3.22@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f AS server
LABEL org.opencontainers.image.source="https://github.com/minio/minio" \
    org.opencontainers.image.revision="9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a" \
    org.opencontainers.image.version="RELEASE.2025-10-15T17-29-55Z"
COPY --from=server-build /out/minio /usr/local/bin/minio
COPY --from=server-build /out/LICENSE /usr/share/licenses/minio/LICENSE
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/minio"]

FROM alpine:3.22@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f AS client
LABEL org.opencontainers.image.source="https://github.com/minio/mc" \
    org.opencontainers.image.revision="7394ce0dd2a80935aded936b09fa12cbb3cb8096" \
    org.opencontainers.image.version="RELEASE.2025-08-13T08-35-41Z"
COPY --from=client-build /out/mc /usr/local/bin/mc
COPY --from=client-build /out/LICENSE /usr/share/licenses/mc/LICENSE
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/mc"]
