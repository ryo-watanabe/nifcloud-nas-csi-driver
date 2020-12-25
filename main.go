/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"os"

	"github.com/golang/glog"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/mount"
	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/cloud"
	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/driver"
	clientset "github.com/kubernetes-csi/external-snapshotter/client/v3/clientset/versioned"
)

var (
	endpoint      = flag.String("endpoint", "unix:/tmp/csi.sock", "CSI endpoint")
	nodeID        = flag.String("nodeid", "", "node id")
	privateIfName = flag.String("privateifname", "ens192", "private network interface name")
	region        = flag.String("region", "jp-east-1", "nifcloud region")
	runController = flag.Bool("controller", false, "run controller service")
	runNode       = flag.Bool("node", false, "run node service")

	version = "v0.0.1"
	revision = "undef"
)

const driverName = "nas.csi.storage.nifcloud.com"

func main() {
	flag.Set("logtostderr", "true")
	flag.Parse()

	glog.Infof("Nifcloud Nas CSI driver version %v %v", version, revision)

	cfg, err := clientcmd.BuildConfigFromFlags("", "")
	if err != nil {
		glog.Fatalf("Error building kubeconfig: %s", err.Error())
	}
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		glog.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}
	snapClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		glog.Fatalf("Error building snapshot clientset: %s", err.Error())
	}

	var provider *cloud.Cloud
	if *runController {
		provider, err = cloud.NewCloud(*region)
		if err != nil {
			glog.Fatalf("Failed to initialize cloud provider: %v", err)
		}
	}
	mounter := mount.New("")
	config := &driver.NifcloudNasDriverConfig{
		Name:          driverName,
		Version:       version,
		NodeID:        *nodeID,
		PrivateIfName: *privateIfName,
		RunController: *runController,
		RunNode:       *runNode,
		Mounter:       mounter,
		Cloud:         provider,
		KubeClient:    kubeClient,
		SnapClient:    snapClient,
	}

	nfnsDriver, err := driver.NewNifcloudNasDriver(config)
	if err != nil {
		glog.Fatalf("Failed to initialize Nifcloud Nas CSI Driver: %v", err)
	}

	glog.Infof("Running driver...")
	nfnsDriver.Run(*endpoint)

	os.Exit(0)
}
