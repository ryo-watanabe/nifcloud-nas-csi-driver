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
	"fmt"
	"os"

	"github.com/golang/glog"
	clientset "github.com/kubernetes-csi/external-snapshotter/client/v4/clientset/versioned"
	"github.com/ryo-watanabe/nifcloud-nas-csi-driver/pkg/cloud"
	"github.com/ryo-watanabe/nifcloud-nas-csi-driver/pkg/driver"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/mount"
)

var (
	endpoint      = flag.String("endpoint", "unix:/tmp/csi.sock", "CSI endpoint")
	nodeID        = flag.String("nodeid", "", "node id")
	privateIfName = flag.String("privateifname", "ens192", "private network interface name")
	region        = flag.String("region", "jp-east-1", "nifcloud region")
	kubeconfig    = flag.String("kubeconfig", "", "kubeconfig file path")
	runController = flag.Bool("controller", false, "run controller service")
	runNode       = flag.Bool("node", false, "run node service")
	privateIPReg  = flag.Bool("privateipreg", false, "get private ip from network interface")
	cidrBlkRcmd   = flag.Bool("recommendcidr", false, "configure recommended cidr block (experimental)")
	cfgSnapRepo   = flag.Bool("cfgsnaprepo", false, "auto configure snapshot repository")
	hatoba        = flag.Bool("hatoba", false, "for clusters of nifcloud hatoba")
	configurator  = flag.Bool("configurator", true, "run configurator")
	restoreClstID = flag.Bool("restoreclstid", true, "restore NASSecurityGroup name on starting of controller")
	devCloudEP    = flag.String("devcloudep", "", "dev cloud endpoint")
	defSnapRegion = flag.String("defaultsnapregion", "jp-east-2", "snapshot objectstore region used in configurator")
	clusterUID    = flag.String("clusteruid", "", "cluster UID default:kube-system namespace UID")

	version  = "v0.6.0c"
	revision = "undef"
)

const driverName = "nas.csi.storage.nifcloud.com"

func main() {
	flagerr := flag.Set("logtostderr", "true")
	if flagerr != nil {
		fmt.Printf("Error in flag.Set : %s\n", flagerr.Error())
	}
	flag.Parse()

	glog.Infof("Nifcloud Nas CSI driver version %v %v", version, revision)

	var cfg *rest.Config
	var err error
	if *kubeconfig == "" {
		cfg, err = rest.InClusterConfig()
		if err != nil {
			glog.Exitf("Error building kubeconfig in cluster: %s", err.Error())
		}
	} else {
		cfg, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			glog.Exitf("Error building kubeconfig out of cluster: %s", err.Error())
		}
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		glog.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}
	snapClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		glog.Fatalf("Error building snapshot clientset: %s", err.Error())
	}

	var provider cloud.Interface
	if *runController {
		provider, err = cloud.NewCloud(*region, *devCloudEP)
		if err != nil {
			glog.Fatalf("Failed to initialize cloud provider: %v", err)
		}
	}
	mounter := mount.New("")
	config := &driver.NifcloudNasDriverConfig{
		Name:              driverName,
		Version:           version,
		NodeID:            *nodeID,
		PrivateIfName:     *privateIfName,
		RunController:     *runController,
		RunNode:           *runNode,
		Mounter:           mounter,
		Cloud:             provider,
		KubeClient:        kubeClient,
		SnapClient:        snapClient,
		PrivateIPReg:      *privateIPReg,
		Configurator:      *configurator,
		RestoreClstID:     *restoreClstID,
		Hatoba:            *hatoba,
		CidrBlkRcmd:       *cidrBlkRcmd,
		CfgSnapRepo:       *cfgSnapRepo,
		DefaultSnapRegion: *defSnapRegion,
		ClusterUID:        *clusterUID,
	}

	nfnsDriver, err := driver.NewNifcloudNasDriver(config)
	if err != nil {
		glog.Fatalf("Failed to initialize Nifcloud Nas CSI Driver: %v", err)
	}

	glog.Infof("Running driver...")
	nfnsDriver.Run(*endpoint)

	os.Exit(0)
}
