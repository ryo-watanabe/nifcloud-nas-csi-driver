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
	"strings"
	"strcnv"

	csi "github.com/container-storage-interface/spec/lib/go/csi/v0"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alice02/nifcloud-sdk-go/service/nas"

	"gitlab.devops.nifcloud.net/x_nke/nfcl-nas-csi-driver/pkg/cloud"
	"gitlab.devops.nifcloud.net/x_nke/nfcl-nas-csi-driver/pkg/util"
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
)

// CreateVolume parameters
const (
	paramInstanceType = "instance-type"
	paramZone = "zone"
	paramNetwork = "network"
	paramReservedIPv4CIDR = "reserved-ipv4-cidr"
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
	glog.V(5).Infof("Using capacity bytes %q for volume %q", capBytes, name)

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
		if reservedIPv4CIDR, ok := req.GetParameters()[paramReservedIPv4CIDR]; ok {
			reservedIP, err := s.reserveIP(ctx, reservedIPv4CIDR)

			// Possible cases are 1) CreateInstanceAborted, 2)CreateInstance running in background
			// The ListInstances response will contain the reservedIPs if the operation was started
			// In case of abort, the IP is released and available for reservation
			defer s.config.ipAllocator.ReleaseIP(reservedIP)
			if err != nil {
				return nil, err
			}

			// Adding the reserved IP to the instance input
			nasInput.MasterPrivateAddress = reservedIP
		} else {
			// If the param was not provided
			return nil, status.Error(codes.InvalidArgument, paramReservedIPv4CIDR + " must be provided")
		}

		// Create the instance
		n, err = s.config.cloud.CreateNasInstance(ctx, nasInput)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	return &csi.CreateVolumeResponse{Volume: fileInstanceToCSIVolume(n, modeInstance)}, nil
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
		cloudInstancesReservedIPs[instance.Endpoint.PrivateAddress] = true
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
	filer, _, err := getFileInstanceFromId(volumeId)
	if err != nil {
		// An invalid ID should be treated as doesn't exist
		glog.V(5).Infof("failed to get instance for volume %v deletion: %v", volumeId, err)
		return &csi.DeleteVolumeResponse{}, nil
	}

	filer.Project = s.config.metaService.GetProject()
	err = s.config.fileService.DeleteInstance(ctx, filer)
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
	filer, _, err := getFileInstanceFromId(volumeId)
	if err != nil {
		// An invalid id format is treated as doesn't exist
		return nil, status.Error(codes.NotFound, err.Error())
	}

	filer.Project = s.config.metaService.GetProject()
	newFiler, err := s.config.fileService.GetInstance(ctx, filer)
	if err != nil && !file.IsNotFoundErr(err) {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if newFiler == nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("volume %v doesn't exist", volumeId))
	}

	// Validate that the volume matches the capabilities
	// Note that there is nothing in the instance that we actually need to validate
	if err := s.config.driver.validateVolumeCapabilities(caps); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{
			Supported: false,
			Message:   err.Error(),
		}, status.Error(codes.InvalidArgument, err.Error())
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Supported: true,
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
func fileInstanceToCSIVolume(n *nas.NASInstance, mode string) *csi.Volume {
	return &csi.Volume{
		Id: s.generateVolumeIdFromNasInstance(n),
		CapacityBytes: strconv.ParseInt(n.AllocatedStorage, 10, 64),
		Attributes: map[string]string{
			attrIp: n.Endpoint.PrivateAddress,
		},
	}
}
