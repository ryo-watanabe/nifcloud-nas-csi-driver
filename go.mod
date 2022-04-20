module github.com/ryo-watanabe/nifcloud-nas-csi-driver

go 1.14

require (
	github.com/aws/aws-sdk-go v1.43.43
	github.com/aws/aws-sdk-go-v2 v0.24.0
	github.com/cenkalti/backoff v2.2.1+incompatible
	github.com/container-storage-interface/spec v1.3.0
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/golang/protobuf v1.4.3
	github.com/imdario/mergo v0.3.7 // indirect
	github.com/kubernetes-csi/csi-lib-utils v0.8.1
	github.com/kubernetes-csi/external-snapshotter/client/v4 v4.2.0
	github.com/nifcloud/nifcloud-sdk-go v1.3.0
	golang.org/x/net v0.0.0-20220127200216-cd36cc0744dd
	golang.org/x/sys v0.0.0-20211216021012-1d35b9e2eb4e
	google.golang.org/grpc v1.33.2
	k8s.io/api v0.19.0
	k8s.io/apimachinery v0.19.0
	k8s.io/client-go v0.19.0
	k8s.io/mount-utils v0.23.5
)
