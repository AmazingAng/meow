# Meow API server. The SQLite driver is pure Go, so the binary is static and
# runs on a distroless base that has CA certificates (needed for APNs) and
# nothing else.
#
#   docker build -t meow-server .
#   docker run -p 8080:8080 -v meow-data:/data meow-server

FROM golang:1.27.2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY client ./client
COPY server ./server
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/meow-server ./server/cmd/meow-server \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/meow-server /meow-server
# An empty /data owned by the nonroot user, so a new named volume is writable.
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV MEOW_ADDR=:8080 \
    MEOW_DB=/data/meow.db
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/meow-server", "healthcheck"]
ENTRYPOINT ["/meow-server"]
