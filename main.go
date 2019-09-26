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

	"k8s.io/kubernetes/pkg/util/mount"
	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/cloud"
	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/driver"
)

var (
	endpoint      = flag.String("endpoint", "unix:/tmp/csi.sock", "CSI endpoint")
	nodeID        = flag.String("nodeid", "", "node id")
	region        = flag.String("region", "jp-east-1", "nifcloud region")
	runController = flag.Bool("controller", false, "run controller service")
	runNode       = flag.Bool("node", false, "run node service")

	version = "v0.0.1"
)

const driverName = "nas.csi.storage.nifcloud.com"

func main() {
	flag.Set("logtostderr", "true")
	flag.Parse()

	var provider *cloud.Cloud
	var err error
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
		RunController: *runController,
		RunNode:       *runNode,
		Mounter:       mounter,
		Cloud:         provider,
	}

	nfnsDriver, err := driver.NewNifcloudNasDriver(config)
	if err != nil {
		glog.Fatalf("Failed to initialize Nifcloud Nas CSI Driver: %v", err)
	}

	glog.Infof("Running Nifcloud Nas CSI driver version %v", version)
	nfnsDriver.Run(*endpoint)

	os.Exit(0)
}
