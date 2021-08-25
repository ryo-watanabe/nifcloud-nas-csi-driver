FROM golang:1.14-alpine as builder

ARG APP_VERSION=undef
ARG APP_REVISION=undef

WORKDIR /app
ADD . .
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=$APP_VERSION -X main.revision=$APP_REVISION"

FROM alpine
RUN apk add --no-cache ca-certificates nfs-utils
COPY --from=builder /app/nifcloud-nas-csi-driver /

ENTRYPOINT ["/nifcloud-nas-csi-driver"]