package driver

import (
	"testing"

	"k8s.io/client-go/kubernetes"
	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/cloud"
)

const testDriverName = "nas.csi.storage.nifcloud.com"

func initTestDriver(t *testing.T, cloud cloud.Interface, kubeClient kubernetes.Interface, runc, runn bool) *NifcloudNasDriver {
	config := &NifcloudNasDriverConfig{
		Name:          "testDriverName",
		Version:       "testDriverVersion",
		//NodeID:        *nodeID,
		//PrivateIfName: *privateIfName,
		RunController: runc,
		RunNode:       runn,
		//Mounter:       mounter,
		Cloud:         newFakeCloud(),
		KubeClient:    kubeClient,
		//SnapClient:    snapClient,
		InitBackoff:   1,
	}

	driver, err := NewNifcloudNasDriver(config)
	if err != nil {
		t.Fatalf("Failed to initialize Nifcloud Nas CSI Driver: %v", err)
	}
	return driver
}
