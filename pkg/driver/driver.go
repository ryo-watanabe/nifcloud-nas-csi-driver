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

package driver

import (
	"fmt"
	"time"

	//csi "github.com/container-storage-interface/spec/lib/go/csi/v0"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/kubernetes/pkg/util/mount"
	"k8s.io/client-go/kubernetes"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/cloud"
)

type NifcloudNasDriverConfig struct {
	Name          string          // Driver name
	Version       string          // Driver version
	NodeID        string          // Node name
	RunController bool            // Run CSI controller service
	RunNode       bool            // Run CSI node service
	Mounter       mount.Interface // Mount library
	Cloud         *cloud.Cloud    // Cloud provider
	KubeClient    kubernetes.Interface  // k8s client
}

type NifcloudNasDriver struct {
	config *NifcloudNasDriverConfig

	// CSI RPC servers
	ids csi.IdentityServer
	ns  csi.NodeServer
	cs  csi.ControllerServer

	// Plugin capabilities
	vcap  map[csi.VolumeCapability_AccessMode_Mode]*csi.VolumeCapability_AccessMode
	cscap []*csi.ControllerServiceCapability
	nscap []*csi.NodeServiceCapability
}

func NewNifcloudNasDriver(config *NifcloudNasDriverConfig) (*NifcloudNasDriver, error) {
	if config.Name == "" {
		return nil, fmt.Errorf("driver name missing")
	}
	if config.Version == "" {
		return nil, fmt.Errorf("driver version missing")
	}
	if config.NodeID == "" {
		return nil, fmt.Errorf("node id missing")
	}
	if config.RunController == false && config.RunNode == false {
		return nil, fmt.Errorf("must run at least one controller or node service")
	}

	driver := &NifcloudNasDriver{
		config: config,
		vcap:   map[csi.VolumeCapability_AccessMode_Mode]*csi.VolumeCapability_AccessMode{},
	}

	vcam := []csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
	}
	driver.addVolumeCapabilityAccessModes(vcam)

	// Setup RPC servers
	driver.ids = newIdentityServer(driver)
	var err error
	if config.RunNode {
		driver.ns, err = newNodeServer(driver, config.Mounter)
		if err != nil {
			return nil, fmt.Errorf("error occured registering private IP: %s", err.Error())
		}
	}
	if config.RunController {
		csc := []csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		}
		driver.addControllerServiceCapabilities(csc)

		// Configure controller server
		driver.cs = newControllerServer(&controllerServerConfig{
			driver: driver,
			cloud: config.Cloud,
		})
	}

	return driver, nil
}

func (driver *NifcloudNasDriver) addVolumeCapabilityAccessModes(vc []csi.VolumeCapability_AccessMode_Mode) error {
	for _, c := range vc {
		glog.Infof("Enabling volume access mode: %v", c.String())
		mode := NewVolumeCapabilityAccessMode(c)
		driver.vcap[mode.Mode] = mode
	}
	return nil
}

func (driver *NifcloudNasDriver) validateVolumeCapabilities(caps []*csi.VolumeCapability) error {
	if len(caps) == 0 {
		return fmt.Errorf("volume capabilities must be provided")
	}

	for _, c := range caps {
		if err := driver.validateVolumeCapability(c); err != nil {
			return err
		}
	}
	return nil
}

func (driver *NifcloudNasDriver) validateVolumeCapability(c *csi.VolumeCapability) error {
	if c == nil {
		return fmt.Errorf("volume capability must be provided")
	}

	// Validate access mode
	accessMode := c.GetAccessMode()
	if accessMode == nil {
		return fmt.Errorf("volume capability access mode not set")
	}
	if driver.vcap[accessMode.Mode] == nil {
		return fmt.Errorf("driver does not support access mode: %v", accessMode.Mode.String())
	}

	// Validate access type
	accessType := c.GetAccessType()
	if accessType == nil {
		return fmt.Errorf("volume capability access type not set")
	}
	mountType := c.GetMount()
	if mountType == nil {
		return fmt.Errorf("driver only supports mount access type volume capability")
	}
	if mountType.FsType != "" {
		// TODO: uncomment after https://github.com/kubernetes-csi/external-provisioner/issues/328 is fixed.
		// return fmt.Errorf("driver does not support fstype %v", mountType.FsType)
	}
	// TODO: check if we want to whitelist/blacklist certain mount options
	return nil
}

func (driver *NifcloudNasDriver) addControllerServiceCapabilities(cl []csi.ControllerServiceCapability_RPC_Type) error {
	var csc []*csi.ControllerServiceCapability
	for _, c := range cl {
		glog.Infof("Enabling controller service capability: %v", c.String())
		csc = append(csc, NewControllerServiceCapability(c))
	}
	driver.cscap = csc
	return nil
}

func (driver *NifcloudNasDriver) addNodeServiceCapabilities(nl []csi.NodeServiceCapability_RPC_Type) error {
	var nsc []*csi.NodeServiceCapability
	for _, n := range nl {
		glog.Infof("Enabling node service capability: %v", n.String())
		nsc = append(nsc, NewNodeServiceCapability(n))
	}
	driver.nscap = nsc
	return nil
}

func (driver *NifcloudNasDriver) ValidateControllerServiceRequest(c csi.ControllerServiceCapability_RPC_Type) error {
	if c == csi.ControllerServiceCapability_RPC_UNKNOWN {
		return nil
	}

	for _, cap := range driver.cscap {
		if c == cap.GetRpc().Type {
			return nil
		}
	}

	return status.Error(codes.InvalidArgument, "Invalid controller service request")
}

func (driver *NifcloudNasDriver) Run(endpoint string) {
	glog.Infof("Running driver: %v", driver.config.Name)

	//Start the nonblocking GRPC
	s := NewNonBlockingGRPCServer()
	s.Start(endpoint, driver.ids, driver.cs, driver.ns)

	// Start NAS Security Group Sync
	if driver.cs != nil {
		syncer := newNSGSyncer(driver)
		go wait.Forever(syncer.runNSGSyncer, time.Duration(syncer.SyncPeriod) * time.Second)
	}

	// Block app : TODO : signal handlings necessary for gracefull stopping.
	s.Wait()
}

func NewVolumeCapabilityAccessMode(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability_AccessMode {
	return &csi.VolumeCapability_AccessMode{Mode: mode}
}

func NewControllerServiceCapability(cap csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
	return &csi.ControllerServiceCapability{
		Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{
				Type: cap,
			},
		},
	}
}

func NewNodeServiceCapability(cap csi.NodeServiceCapability_RPC_Type) *csi.NodeServiceCapability {
	return &csi.NodeServiceCapability{
		Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{
				Type: cap,
			},
		},
	}
}
