FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/tally ./cmd/tally \
 && mkdir -m 1777 /out/tmp && mkdir /out/data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/tmp /tmp
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/tally /tally
USER 65532:65532
ENV TALLY_ADDR=:8080 TALLY_DB=/data/tally.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/tally"]
CMD ["serve"]
