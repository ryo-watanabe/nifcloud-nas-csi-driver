FROM golang:1.14-alpine as builder

ARG APP_VERSION=undef
ARG APP_REVISION=undef

WORKDIR /go/src/github.com/ryo-watanabe/nifcloud-nas-csi-driver/

RUN apk --no-cache add curl tar git ca-certificates && \
    update-ca-certificates

ADD . .

RUN CGO_ENABLED=0 go build -ldflags "-X main.version=$APP_VERSION -X main.revision=$APP_REVISION"

FROM alpine

COPY --from=builder /go/src/github.com/ryo-watanabe/nifcloud-nas-csi-driver/nifcloud-nas-csi-driver /
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENTRYPOINT ["/nifcloud-nas-csi-driver"]