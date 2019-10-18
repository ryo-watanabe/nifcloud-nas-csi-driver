FROM alpine
RUN apk add --no-cache ca-certificates nfs-utils
COPY nfcl-nas-csi-driver /
