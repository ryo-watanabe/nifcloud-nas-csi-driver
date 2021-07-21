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

	"github.com/cenkalti/backoff"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	clientset "github.com/kubernetes-csi/external-snapshotter/client/v4/clientset/versioned"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/mount"

	"github.com/ryo-watanabe/nifcloud-nas-csi-driver/pkg/cloud"
)

// NifcloudNasDriverConfig holds CSI options
type NifcloudNasDriverConfig struct {
	Name              string               // Driver name
	Version           string               // Driver version
	NodeID            string               // Node name
	PrivateIfName     string               // Private network interface name
	RunController     bool                 // Run CSI controller service
	RunNode           bool                 // Run CSI node service
	Mounter           mount.Interface      // Mount library
	Cloud             cloud.Interface      // Cloud provider
	KubeClient        kubernetes.Interface // k8s client
	SnapClient        clientset.Interface  // snapshot client
	InitBackoff       time.Duration
	PrivateIPReg      bool
	Configurator      bool
	RestoreClstID     bool
	Hatoba            bool
	CidrBlkRcmd       bool
	CfgSnapRepo       bool
	DefaultSnapRegion string
}

// NifcloudNasDriver a CSI driver
type NifcloudNasDriver struct {
	config *NifcloudNasDriverConfig

	// CSI RPC servers
	ids csi.IdentityServer
	ns  csi.NodeServer
	cs  csi.ControllerServer
	sv  NonBlockingGRPCServer

	// Plugin capabilities
	vcap  map[csi.VolumeCapability_AccessMode_Mode]*csi.VolumeCapability_AccessMode
	cscap []*csi.ControllerServiceCapability
	nscap []*csi.NodeServiceCapability
}

// NewNifcloudNasDriver creates a new driver
func NewNifcloudNasDriver(config *NifcloudNasDriverConfig) (*NifcloudNasDriver, error) {
	if config.Name == "" {
		return nil, fmt.Errorf("driver name missing")
	}
	if config.Version == "" {
		return nil, fmt.Errorf("driver version missing")
	}
	if config.NodeID == "" && config.RunNode {
		return nil, fmt.Errorf("node id missing")
	}
	if !config.RunController && !config.RunNode {
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
	err := driver.addVolumeCapabilityAccessModes(vcam)
	if err != nil {
		return nil, fmt.Errorf("error in addVolumeCapabilityAccessModes : %s", err.Error())
	}

	// Setup RPC servers
	driver.ids = newIdentityServer(driver)
	if config.RunNode {
		nsc := []csi.NodeServiceCapability_RPC_Type{
			csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		}
		err = driver.addNodeServiceCapabilities(nsc)
		if err != nil {
			return nil, fmt.Errorf("error in addNodeServiceCapabilities : %s", err.Error())
		}

		driver.ns = newNodeServer(driver, config.Mounter)
	}
	if config.RunController {
		csc := []csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
			csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
		}
		err = driver.addControllerServiceCapabilities(csc)
		if err != nil {
			return nil, fmt.Errorf("error in addControllerServiceCapabilities : %s", err.Error())
		}

		// Configure controller server
		driver.cs = newControllerServer(&controllerServerConfig{
			driver: driver,
			cloud:  config.Cloud,
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
	//if mountType.FsType != "" {
	// 	TODO: uncomment after https://github.com/kubernetes-csi/external-provisioner/issues/328 is fixed.
	// 	return fmt.Errorf("driver does not support fstype %v", mountType.FsType)
	//}
	// TODO: check if we want to whitelist/blacklist certain mount options
	return nil
}

func (driver *NifcloudNasDriver) addControllerServiceCapabilities(cl []csi.ControllerServiceCapability_RPC_Type) error {
	csc := []*csi.ControllerServiceCapability{}
	for _, c := range cl {
		glog.Infof("Enabling controller service capability: %v", c.String())
		csc = append(csc, NewControllerServiceCapability(c))
	}
	driver.cscap = csc
	return nil
}

func (driver *NifcloudNasDriver) addNodeServiceCapabilities(nl []csi.NodeServiceCapability_RPC_Type) error {
	nsc := []*csi.NodeServiceCapability{}
	for _, n := range nl {
		glog.Infof("Enabling node service capability: %v", n.String())
		nsc = append(nsc, NewNodeServiceCapability(n))
	}
	driver.nscap = nsc
	return nil
}

// ValidateControllerServiceRequest validates controller capabilities
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

func retryNotify(err error, wait time.Duration) {
	glog.Infof("Retrying after %.2f seconds : %s", wait.Seconds(), err.Error())
}

// Run starts the driver server
func (driver *NifcloudNasDriver) Run(endpoint string) {
	glog.Infof("Running driver: %v", driver.config.Name)

	//Start the nonblocking GRPC
	driver.sv = NewNonBlockingGRPCServer()
	driver.sv.Start(endpoint, driver.ids, driver.cs, driver.ns)

	// Start Configurator and NAS SecurityGroup sync on controller
	if driver.cs != nil {
		// Configurator
		if driver.config.Configurator {
			conf := newConfigurator(driver)
			err := conf.Init()
			if err != nil {
				glog.Errorf("Error in Configurator initilization : %s", err.Error())
			}
			go wait.Forever(conf.runUpdate, time.Duration(conf.chkIntvl)*time.Second)
		}

		// Security Group syncer
		syncer := newNSGSyncer(driver)
		go wait.Forever(syncer.runNSGSyncer, time.Duration(syncer.SyncPeriod)*time.Second)
	}

	// Register node private IP
	if driver.ns != nil && driver.config.PrivateIPReg {
		b := backoff.NewExponentialBackOff()
		b.MaxElapsedTime = time.Duration(60) * time.Second
		b.RandomizationFactor = 0.2
		b.Multiplier = 1.0
		b.InitialInterval = 10 * time.Second
		if driver.config.InitBackoff != 0 {
			b.InitialInterval = driver.config.InitBackoff * time.Second
		}
		registerNodePrivateIPFunc := func() error {
			return registerNodePrivateIP(driver.config)
		}
		err := backoff.RetryNotify(registerNodePrivateIPFunc, b, retryNotify)
		if err != nil {
			glog.Fatalf("Exit by error in registering node private IP: %s", err.Error())
		}
	}

	// Block app : TODO : signal handlings necessary for graceful stopping.
	driver.sv.Wait()
}

// Stop stops the driver server
func (driver *NifcloudNasDriver) Stop() {
	if driver.sv != nil {
		driver.sv.Stop()
	}
}

// NewVolumeCapabilityAccessMode sets access modes
func NewVolumeCapabilityAccessMode(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability_AccessMode {
	return &csi.VolumeCapability_AccessMode{Mode: mode}
}

// NewControllerServiceCapability sets controller service capabilities
func NewControllerServiceCapability(cap csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
	return &csi.ControllerServiceCapability{
		Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{
				Type: cap,
			},
		},
	}
}

// NewNodeServiceCapability sets node service capabilities
func NewNodeServiceCapability(cap csi.NodeServiceCapability_RPC_Type) *csi.NodeServiceCapability {
	return &csi.NodeServiceCapability{
		Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{
				Type: cap,
			},
		},
	}
}
