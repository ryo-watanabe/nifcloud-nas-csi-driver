
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
	"strconv"
	"reflect"

	//csi "github.com/container-storage-interface/spec/lib/go/csi/v0"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	//"github.com/cenkalti/backoff"
	//"sigs.k8s.io/sig-storage-lib-external-provisioner/controller"

	"github.com/alice02/nifcloud-sdk-go-v2/service/nas"

	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/cloud"
	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/util"
)

const (
	// premium tier min is 2.5 Tb, let GCFS error
	minVolumeSize     int64 = 100 * util.Gb
	//modeInstance            = "modeInstance"
	//newInstanceVolume       = "vol1"

	//defaultTier    = "standard"
	//defaultNetwork = "default"
)

// Volume attributes
const (
	attrIp     = "ip"
	attrVolume = "volume"
)

// controllerServer handles volume provisioning
type controllerServer struct {
	config *controllerServerConfig
}

type controllerServerConfig struct {
	driver *NifcloudNasDriver
	cloud *cloud.Cloud
	//metaService metadata.Service
	ipAllocator *util.IPAllocator
}

func newControllerServer(config *controllerServerConfig) csi.ControllerServer {
	config.ipAllocator = util.NewIPAllocator(make(map[string]bool))
	return &controllerServer{config: config}
}

/*
func retryNotify(err error, wait time.Duration) {
	glog.V(4).Infof("Retrying after %.2f seconds : %s", wait.Seconds(), err.Error())
}
*/

type NSGSyncer struct {
	driver *NifcloudNasDriver
	SyncPeriod int64
	internalChkIntvl int64
	cloudChkIntvl int64
	hasTask bool
	lastNodePrivateIps *map[string]bool
	lastSecurityGroupInputs *map[string]nas.CreateNASSecurityGroupInput
}

func newNSGSyncer(driver *NifcloudNasDriver) *NSGSyncer {
	return &NSGSyncer{
		driver:  driver,
		SyncPeriod: 30, // seconds
		internalChkIntvl: 300, // seconds
		cloudChkIntvl: 3600, // seconds
		hasTask: true,
		lastNodePrivateIps: nil,
		lastSecurityGroupInputs: nil,
	}
}

func (s *NSGSyncer) runNSGSyncer() {
	err := s.SyncNasSecurityGroups()
	if err != nil {
		runtime.HandleError(err)
	}
}

// Sync StorageClass resource and NasSecurityGrooup
func (s *NSGSyncer) SyncNasSecurityGroups() error {

	// Skip some syncs when current tasks seem to be done.
	p := time.Now().Unix() / s.SyncPeriod
	doInternalChk := ( p % (s.internalChkIntvl/s.SyncPeriod) == 0 )
	doCloudChk := ( p % (s.cloudChkIntvl/s.SyncPeriod) == 0 )
	glog.V(5).Infof("SyncNasSecurityGroups %d internal check:%v cloud check:%v", p, doInternalChk, doCloudChk)

	if !s.hasTask && !doInternalChk && !doCloudChk {
		return nil
	}

	glog.V(5).Infof("SyncNasSecurityGroups internal check")

	kubeClient := s.driver.config.KubeClient
	ctx := context.TODO()

	// Nodes' private IPs
	csinodes, err := kubeClient.StorageV1beta1().CSINodes().List(metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("Error getting node private IPs: %s", err.Error())
	}
	nodePrivateIps := make(map[string]bool, 0)
	for _, node := range csinodes.Items {
		annotations := node.ObjectMeta.GetAnnotations()
		if annotations != nil {
			privateIp := annotations[s.driver.config.Name + "/privateIp"]
			if privateIp != "" {
				nodePrivateIps[privateIp] = true
			}
		}
	}
	glog.V(5).Infof("nodePrivateIps : %v", nodePrivateIps)

	// NAS Security Groups
	classes, err := kubeClient.StorageV1().StorageClasses().List(metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("Error getting storage class: %s", err.Error())
	}
	securityGroupInputs := make(map[string]nas.CreateNASSecurityGroupInput, 0)
	for _, class := range classes.Items {
		if class.Provisioner == s.driver.config.Name {
			name := class.Parameters["securityGroup"]
			zone := class.Parameters["zone"]
			if name != "" && zone != "" {
				securityGroupInputs[name] = nas.CreateNASSecurityGroupInput{
					AvailabilityZone: &zone,
					NASSecurityGroupName: &name,
				}
			}
		}
	}
	glog.V(5).Infof("securityGroupInputs : %v", securityGroupInputs)

	// Internal check
	if s.lastNodePrivateIps != nil && s.lastSecurityGroupInputs != nil {
		if reflect.DeepEqual(nodePrivateIps, *s.lastNodePrivateIps) &&
		   reflect.DeepEqual(securityGroupInputs, *s.lastSecurityGroupInputs) {
			if !s.hasTask && !doCloudChk {
				return nil
			}
		} else {
			// Nodes or storage classes changed.
			glog.V(4).Infof("SyncNasSecurityGroups nodes or storage classes changed")
			s.lastNodePrivateIps = &nodePrivateIps
			s.lastSecurityGroupInputs = &securityGroupInputs
			s.hasTask = true
		}
	} else {
		// Nodes or storage classes changed.
		s.lastNodePrivateIps = &nodePrivateIps
		s.lastSecurityGroupInputs = &securityGroupInputs
		s.hasTask = true
	}

	glog.V(5).Infof("SyncNasSecurityGroups cloud check")

	// Check cloud and synchronize
	for _, scInput := range securityGroupInputs {

		nsg, err := s.driver.config.Cloud.GetNasSecurityGroup(ctx, *scInput.NASSecurityGroupName)

		// Check if the NAS Security Group exists and create one if not.
		if err != nil {
			if cloud.IsNotFoundErr(err) {
				nsg, err = s.driver.config.Cloud.CreateNasSecurityGroup(ctx, &scInput)
				if err != nil {
					return fmt.Errorf("Error creating NASSecurityGroup: %s", err.Error())
				}
				glog.V(4).Infof("NAS SecurityGroup %s created", *scInput.NASSecurityGroupName)
			} else {
				return fmt.Errorf("Error getting NASSecurityGroup: %s", err.Error())
			}
		}

		// Authorize node private ip if not.
		for ip, _ := range nodePrivateIps {
			authorized := false
			iprange := ip + "/32"
			for _, aip := range nsg.IPRanges {
				if *aip.CIDRIP == iprange {
					authorized = true
					break
				}
			}
			if !authorized {
				s.hasTask = true
				_, err = s.driver.config.Cloud.AuthorizeCIDRIP(ctx, *scInput.NASSecurityGroupName, iprange)
				if err != nil {
					return fmt.Errorf("Error authorizing NASSecurityGroup ingress: %s", err.Error())
				}
				glog.V(4).Infof("CIDRIP %s authorized in SecurityGroup %s", iprange, *scInput.NASSecurityGroupName)
				// Do one task only to wait recovery of Client.Resource.IncorrectState.ApplyNASSecurityGroup.
				return nil
			}
		}

		// Revoke CIDRIPs not in node private IPs.
		for _, aip := range nsg.IPRanges {
			nodeExists := false
			for ip, _ := range nodePrivateIps {
				iprange := ip + "/32"
				if *aip.CIDRIP == iprange {
					nodeExists = true
					break
				}
			}
			if !nodeExists {
				s.hasTask = true
				_, err = s.driver.config.Cloud.RevokeCIDRIP(ctx, *scInput.NASSecurityGroupName, *aip.CIDRIP)
				if err != nil {
					return fmt.Errorf("Error revoking NASSecurityGroup ingress: %s", err.Error())
				}
				glog.V(4).Infof("CIDRIP %s revoked in SecurityGroup %s", aip.String(), *scInput.NASSecurityGroupName)
				// Do one task only to wait recovery of Client.Resource.IncorrectState.ApplyNASSecurityGroup.
				return nil
			}
		}
	}

	// Tasks completed here
	s.hasTask = false

	return nil
}

// CreateVolume creates a GCFS instance
func (s *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	glog.V(4).Infof("CreateVolume called with request %v", *req)

	// Validate arguments
	name := req.GetName()
	if len(name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume name must be provided")
	}

	if err := s.config.driver.validateVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	capBytes := getRequestCapacity(req.GetCapacityRange())
	glog.V(4).Infof("Using capacity bytes %q for volume %q", capBytes, name)

	nasInput, err := cloud.GenerateNasInstanceInput(name, capBytes, req.GetParameters())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Check if the instance already exists
	n, err := s.config.cloud.GetNasInstance(ctx, name)
	if err != nil && !cloud.IsNotFoundErr(err) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if n != nil {
		// Instance already exists, check if it meets the request
		if err = cloud.CompareNasInstanceWithInput(n, nasInput); err != nil {
			return nil, status.Error(codes.AlreadyExists, err.Error())
		}

	} else {
		// If we are creating a new instance, we need pick an unused ip from reserved-ipv4-cidr
		if reservedIPv4CIDR, ok := req.GetParameters()["reservedIpv4Cidr"]; ok {
			reservedIP, err := s.reserveIP(ctx, reservedIPv4CIDR)

			// Possible cases are 1) CreateInstanceAborted, 2)CreateInstance running in background
			// The ListInstances response will contain the reservedIPs if the operation was started
			// In case of abort, the IP is released and available for reservation
			defer s.config.ipAllocator.ReleaseIP(reservedIP)
			if err != nil {
				return nil, err
			}
			reservedIP = reservedIP + "/24"

			// Adding the reserved IP to the instance input
			nasInput.MasterPrivateAddress = &reservedIP
		} else {
			// If the param was not provided
			return nil, status.Error(codes.InvalidArgument, "reservedIpv4Cidr must be provided")
		}

		// Create the instance
		n, err = s.config.cloud.CreateNasInstance(ctx, nasInput)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}

		/*
		// do snapshot with backoff retry
		b := backoff.NewExponentialBackOff()
		b.MaxElapsedTime = time.Duration(10) * time.Minute
		b.RandomizationFactor = 0.2
		b.Multiplier = 1.0
		b.InitialInterval = 30 * time.Second
		backoffCtx := context.TODO()

		chkNasInstanceReady := func() error {
			chkNas, err := s.config.cloud.GetNasInstance(backoffCtx, name)
			if err != nil {
				return backoff.Permanent(err)
			}
			if *chkNas.NASInstanceStatus == "available" {
				return nil
			}
			return fmt.Errorf("NASInstanceStatus : %s", *chkNas.NASInstanceStatus)
		}
		err = backoff.RetryNotify(chkNasInstanceReady, b, retryNotify)
		if err != nil {
			return nil, status.Error(codes.Internal, "Error in waiting for NASInstance becomes ready : " + err.Error())
		}*/

	}

	if *n.NASInstanceStatus != "available" {
		msg := fmt.Sprintf("NAS Instance %s already exists but not available : %s", name, *n.NASInstanceStatus)
		glog.V(4).Infof(msg)
		if *n.NASInstanceStatus == "creating" {
			// Returning codes.Aborted will let controller to retry.
			// This causes warning events in PVC. Is there better way to ask controller to retry ?
			return nil, status.Error(codes.Aborted, msg)
		} else {
			// Returning codes.Internal will let controller NOT to retry.
			return nil, status.Error(codes.Internal, msg)
		}
	}

	// Set no_root_squash and get NAS Instance again
	n, err = s.config.cloud.ModifyNasInstance(ctx, name)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.CreateVolumeResponse{Volume: s.nasInstanceToCSIVolume(n)}, nil
}

// reserveIPRange returns the available IP in the cidr
func (s *controllerServer) reserveIP(ctx context.Context, cidr string) (string, error) {
	cloudInstancesReservedIPs, err := s.getCloudInstancesReservedIPs(ctx)
	if err != nil {
		return "", err
	}
	unreservedIP, err := s.config.ipAllocator.GetUnreservedIP(cidr, cloudInstancesReservedIPs)
	if err != nil {
		return "", err
	}
	return unreservedIP, nil
}

// getCloudInstancesReservedIPRanges gets the list of reservedIPRanges from cloud instances
func (s *controllerServer) getCloudInstancesReservedIPs(ctx context.Context) (map[string]bool, error) {
	instances, err := s.config.cloud.ListNasInstances(ctx)
	if err != nil {
		return nil, status.Error(codes.Aborted, err.Error())
	}
	// Initialize an empty reserved list. It will be populated with all the reservedIPRanges obtained from the cloud instances
	cloudInstancesReservedIPs := make(map[string]bool)
	for _, instance := range instances {
		cloudInstancesReservedIPs[*instance.Endpoint.PrivateAddress] = true
	}
	return cloudInstancesReservedIPs, nil
}

// DeleteVolume deletes a GCFS instance
func (s *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	glog.V(4).Infof("DeleteVolume called with request %v", *req)

	volumeId := req.GetVolumeId()
	if volumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is empty")
	}
	nas, err := s.config.cloud.GetNasInstanceFromVolumeId(ctx, volumeId)
	if err != nil {
		// An invalid ID should be treated as doesn't exist
		glog.V(5).Infof("failed to get instance for volume %v deletion: %v", volumeId, err)
		return &csi.DeleteVolumeResponse{}, nil
	}

	err = s.config.cloud.DeleteNasInstance(ctx, *nas.NASInstanceIdentifier)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.DeleteVolumeResponse{}, nil
}

func (s *controllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	// Validate arguments
	volumeId := req.GetVolumeId()
	if volumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is empty")
	}
	caps := req.GetVolumeCapabilities()
	if len(caps) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities is empty")
	}

	// Check that the volume exists
	_, err := s.config.cloud.GetNasInstanceFromVolumeId(ctx, volumeId)
	if err != nil {
		// An invalid id format is treated as doesn't exist
		return nil, status.Error(codes.NotFound, err.Error())
	}

	// Validate that the volume matches the capabilities
	// Note that there is nothing in the instance that we actually need to validate
	if err := s.config.driver.validateVolumeCapabilities(caps); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{
			//Supported: false,
			Message:   err.Error(),
		}, status.Error(codes.InvalidArgument, err.Error())
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		//Supported: true,
	}, nil
}

func (s *controllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: s.config.driver.cscap,
	}, nil
}

// getRequestCapacity returns the volume size that should be provisioned
func getRequestCapacity(capRange *csi.CapacityRange) int64 {
	if capRange == nil {
		return minVolumeSize
	}

	rCap := capRange.GetRequiredBytes()
	lCap := capRange.GetLimitBytes()

	if lCap > 0 {
		if rCap == 0 {
			// request not set
			return lCap
		} else {
			// request set, round up to min
			return util.Min(util.Max(rCap, minVolumeSize), lCap)
		}
	}

	// limit not set
	return util.Max(rCap, minVolumeSize)
}

// fileInstanceToCSIVolume generates a CSI volume spec from the cloud Instance
func (s *controllerServer) nasInstanceToCSIVolume(n *nas.NASInstance) *csi.Volume {
	capacityGBytes, _ := strconv.ParseInt(*n.AllocatedStorage, 10, 64)
	ip := "0.0.0.0"
	if n.Endpoint != nil && n.Endpoint.PrivateAddress != nil {
		ip = *n.Endpoint.PrivateAddress
	}
	return &csi.Volume{
		VolumeId: s.config.cloud.GenerateVolumeIdFromNasInstance(n),
		CapacityBytes: util.GbToBytes(capacityGBytes),
		VolumeContext: map[string]string{
			attrIp: ip,
		},
	}
}

///// Not implemented methods

func (s *controllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerPublishVolume unsupported")
}

func (s *controllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerUnpublishVolume unsupported")
}

func (s *controllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	// https://cloud.google.com/compute/docs/reference/beta/disks/list
	// List volumes in the whole region? In only the zone that this controller is running?
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *controllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	// https://cloud.google.com/compute/quotas
	// DISKS_TOTAL_GB.
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *controllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "CreateSnapshot unsupported")
}

func (s *controllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "DeleteSnapshot unsupported")
}

func (s *controllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListSnapshots unsupported")
}

func (s *controllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerExpandVolume unsupported")
}