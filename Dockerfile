FROM alpine
RUN apk add --no-cache ca-certificates nfs-utils
COPY nifcloud-nas-csi-driver /
ENTRYPOINT ["/nifcloud-nas-csi-driver"]
