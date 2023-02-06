module github.com/ryo-watanabe/nifcloud-nas-csi-driver

go 1.14

require (
	github.com/aws/aws-sdk-go v1.44.195
	github.com/aws/aws-sdk-go-v2 v0.24.0
	github.com/cenkalti/backoff v2.2.1+incompatible
	github.com/container-storage-interface/spec v1.3.0
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/golang/protobuf v1.4.3
	github.com/imdario/mergo v0.3.7 // indirect
	github.com/kubernetes-csi/csi-lib-utils v0.8.1
	github.com/kubernetes-csi/external-snapshotter/client/v4 v4.2.0
	github.com/nifcloud/nifcloud-sdk-go v1.3.0
	golang.org/x/net v0.1.0
	golang.org/x/sys v0.3.0
	google.golang.org/grpc v1.33.2
	k8s.io/api v0.20.0
	k8s.io/apimachinery v0.20.0
	k8s.io/client-go v0.20.0
	k8s.io/mount-utils v0.26.1
)
