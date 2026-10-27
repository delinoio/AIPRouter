# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download all
RUN apt-get update && apt-get install -y --no-install-recommends python3 && rm -rf /var/lib/apt/lists/*
COPY scripts/aiprouter/licenses.py /src/licenses.py
RUN python3 -c 'from pathlib import Path; from licenses import bundle; bundle(Path("/src"), Path("/out-notices"))'
COPY servers/aiprouter ./servers/aiprouter
COPY packages/aiprouter ./packages/aiprouter
COPY protos ./protos
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/aiprouter-server ./servers/aiprouter
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/aiprouter-server /usr/local/bin/aiprouter-server
COPY LICENSE NOTICE /usr/share/doc/aiprouter/
COPY --from=build /out-notices /usr/share/doc/aiprouter/THIRD_PARTY_NOTICES
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["aiprouter-server"]
CMD ["serve"]
