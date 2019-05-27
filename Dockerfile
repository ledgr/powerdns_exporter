FROM golang:alpine AS build-env

ADD . /go/src/powerdns_exporter
WORKDIR /go/src/powerdns_exporter

RUN apk add --no-cache git
RUN go install -v -ldflags "-X main.programVersion=$(git describe --tags || git rev-parse --short HEAD || echo dev)" ./...

FROM alpine:latest
COPY --from=build-env /go/bin/powerdns_exporter /powerdns_exporter

ENTRYPOINT ["/powerdns_exporter"]

EXPOSE 9120
