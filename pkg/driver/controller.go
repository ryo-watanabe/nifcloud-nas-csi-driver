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
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/nifcloud/nifcloud-sdk-go/service/nas"
	"github.com/ryo-watanabe/nifcloud-nas-csi-driver/pkg/cloud"
	"github.com/ryo-watanabe/nifcloud-nas-csi-driver/pkg/util"
)

const (
	minVolumeSize int64 = 100 * util.Gb
)

// Volume attributes
const (
	attrIP         = "ip"
	attrSourcePath = "path"
)

// controllerServer handles volume provisioning
type controllerServer struct {
	config *controllerServerConfig
}

type controllerServerConfig struct {
	driver           *NifcloudNasDriver
	cloud            cloud.Interface
	ipAllocator      *IPAllocator
	nasNameHolder    *InstanceNameHolder
	volumeInformer   cache.SharedInformer
	creatingPvsQueue *OperateResourceQueue
	deletingPvsQueue *OperateResourceQueue
}

func newControllerServer(config *controllerServerConfig) csi.ControllerServer {
	config.ipAllocator = newIPAllocator(make(map[string]bool))
	config.nasNameHolder = newInstanceNameHolder()
	config.creatingPvsQueue = newOperateResourceQueue("Creating PVs queue")
	config.deletingPvsQueue = newOperateResourceQueue("Deleting PVs queue")
	controller := &controllerServer{config: config}

	// Prepare PersistenVolume informer
	informer := informers.NewSharedInformerFactory(config.driver.config.KubeClient, time.Second*10)
	volumeHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { controller.pvAdded(obj) },
		//UpdateFunc: func(oldObj, newObj interface{}) { controller.pvUpdated(newObj) },
		DeleteFunc: func(obj interface{}) { controller.pvDeleted(obj) },
	}
	controller.config.volumeInformer = informer.Core().V1().PersistentVolumes().Informer()
	controller.config.volumeInformer.AddEventHandler(volumeHandler)

	// Run informer here because controller could not have Run func. Is it right?
	ctx := context.TODO()
	informer.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), controller.config.volumeInformer.HasSynced) {
		glog.Errorf("Error in starting volume informer")
	}

	return controller
}

// Called when PersistentVolume resource created
func (s *controllerServer) pvAdded(obj interface{}) {
	var key string
	var err error
	if key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	glog.V(4).Infof("PersistentVolume %s added", key)
	s.config.creatingPvsQueue.UnsetQueue(key)
}

// Called when PersistentVolume resource deleted
func (s *controllerServer) pvDeleted(obj interface{}) {
	var key string
	var err error
	if key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	glog.V(4).Infof("PersistentVolume %s deleted", key)
	s.config.deletingPvsQueue.UnsetQueue(key)
}

// Get reserved CIDRIP and permission from PVC's annotations
func getAnnotationsFromPVC(ctx context.Context, name string, driver *NifcloudNasDriver) (string, string, error) {
	kubeClient := driver.config.KubeClient
	pvcUIDs := strings.SplitN(name, "-", 2)
	if len(pvcUIDs) < 2 {
		return "", "", fmt.Errorf("Error getting IP from PVC : Cannot split UID")
	}
	pvcs, err := kubeClient.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", "", fmt.Errorf("Error getting PVC list : %s", err.Error())
	}
	for _, pvc := range pvcs.Items {
		if string(pvc.ObjectMeta.GetUID()) == pvcUIDs[1] {
			cidrip := ""
			permission := ""
			if value, ok := pvc.ObjectMeta.GetAnnotations()[driver.config.Name+"/reservedIPv4Cidr"]; ok {
				if !strings.Contains(value, "/") {
					value = value + "/32"
				}
				err = validateCIDRIP(value)
				if err != nil {
					return "", "", fmt.Errorf("Error getting PVC annotation : %s", err.Error())
				}
				cidrip = value
			}
			if value, ok := pvc.ObjectMeta.GetAnnotations()[driver.config.Name+"/permission"]; ok {
				err = validatePermission(value)
				if err != nil {
					return "", "", fmt.Errorf("Error getting PVC annotation : %s", err.Error())
				}
				permission = value
			}
			return cidrip, permission, nil
		}
	}
	return "", "", fmt.Errorf("Error PVC %s not found", pvcUIDs[1])
}

func (s *controllerServer) waitForNASInstanceAvailable(name string) error {
	// wait for NAS available with backoff retry
	b := backoff.NewExponentialBackOff()
	b.MaxElapsedTime = time.Duration(30) * time.Minute
	b.RandomizationFactor = 0.2
	b.Multiplier = 1.0
	b.InitialInterval = 30 * time.Second
	if s.config.driver.config.InitBackoff != 0 {
		b.InitialInterval = s.config.driver.config.InitBackoff * time.Second
	}
	backoffCtx := context.TODO()

	chkNasInstanceAvailable := func() error {
		chkNas, err := s.config.cloud.GetNasInstance(backoffCtx, name)
		if err != nil {
			return backoff.Permanent(err)
		}
		if *chkNas.NASInstanceStatus == "available" {
			return nil
		} else if *chkNas.NASInstanceStatus != "creating" && *chkNas.NASInstanceStatus != "modifying" {
			return backoff.Permanent(fmt.Errorf("NASInstance %s status: %s", name, *chkNas.NASInstanceStatus))
		}
		return fmt.Errorf("NASInstance %s status: %s", name, *chkNas.NASInstanceStatus)
	}

	return backoff.RetryNotify(chkNasInstanceAvailable, b, retryNotify)
}

// NAS capacity string
func getAllocatedStorage(capBytes int64, instanceType int64) *int64 {

	var allocatedStorage int64
	allocatedStorage = 100
	if instanceType == 1 {
		allocatedStorage = 1000
	}

	for i := 1; i < 10; i++ {
		if instanceType == 1 {
			allocatedStorage = int64(i) * 1000
		} else {
			allocatedStorage = int64(i) * 100
		}
		if util.GbToBytes(allocatedStorage) >= capBytes {
			break
		}
	}

	return &allocatedStorage
}

// GenerateNasInstanceInput generates CreateVolume parameters
func GenerateNasInstanceInput(
	name string, capBytes int64, params map[string]string) (*nas.CreateNASInstanceInput, error) {
	// Set default parameters
	var instanceType int64
	instanceType = 0
	zone := "east-11"
	network := "net-COMMON_PRIVATE"
	protocol := "nfs"
	description := "csi-volume"
	var err error

	// Validate parameters (case-insensitive).
	for k, v := range params {
		switch strings.ToLower(k) {
		case "instancetype":
			instanceType, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid parameter %q", k)
			}
		case "zone":
			zone = v
		case "networkid":
			network = v
		case "description":
			description = v
		case "reservedipv4cidr", "capacityparinstancegib", "shared":
			// allowed
		default:
			return nil, fmt.Errorf("invalid parameter %q", k)
		}
	}
	return &nas.CreateNASInstanceInput{
		AllocatedStorage: getAllocatedStorage(capBytes, instanceType),
		AvailabilityZone: &zone,
		//MasterPrivateAddress: must set after
		NASInstanceIdentifier:  &name,
		NASInstanceType:        &instanceType,
		NASInstanceDescription: &description,
		//NASSecurityGroups: must set after
		NetworkId: &network,
		Protocol:  &protocol,
	}, nil
}

func compareNasInstanceWithInput(n *nas.NASInstance, in *nas.CreateNASInstanceInput) error {
	mismatches := []string{}
	if *n.NASInstanceType != *in.NASInstanceType {
		mismatches = append(mismatches, "NASInstanceType")
	}
	allocatedStorage, _ := strconv.ParseInt(pstr(n.AllocatedStorage), 10, 64)
	if allocatedStorage != *in.AllocatedStorage {
		mismatches = append(mismatches, "AllocatedStorage")
	}
	if pstr(n.NetworkId) != pstr(in.NetworkId) {
		mismatches = append(mismatches, "NetworkId")
	}
	if pstr(n.AvailabilityZone) != pstr(in.AvailabilityZone) {
		mismatches = append(mismatches, "AvailabilityZone")
	}
	if len(n.NASSecurityGroups) != len(in.NASSecurityGroups) {
		mismatches = append(mismatches, "Number of NASSecurityGroups")
		for i := 0; i < len(n.NASSecurityGroups); i++ {
			if *n.NASSecurityGroups[i].NASSecurityGroupName != in.NASSecurityGroups[i] {
				mismatches = append(mismatches, "NASSecurityGroupName")
			}
		}
	}

	if len(mismatches) > 0 {
		return fmt.Errorf("instance %v already exists but doesn't match expected: %+v", n.NASInstanceIdentifier, mismatches)
	}
	return nil
}

// CreateVolume creates a NAS instance
func (s *controllerServer) CreateVolume(
	ctx context.Context, req *csi.CreateVolumeRequest) (res *csi.CreateVolumeResponse, err error) {
	glog.V(4).Infof("CreateVolume called with request %v", *req)

	res = nil

	// Validate arguments
	name := req.GetName()
	if len(name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume name must be provided")
	}

	if err = s.config.driver.validateVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// If request is for shared NAS, name will be converted into shared NAS name
	// and request's name will be used as source path in shared NAS
	pvName := name
	defer func() {
		if err != nil {
			s.config.creatingPvsQueue.UnsetQueue(pvName)
		}
	}()
	name, capBytes, err := s.convertSharedRequest(ctx, name, req)
	if err != nil {
		err = status.Error(codes.InvalidArgument, err.Error())
		return
	}
	s.config.creatingPvsQueue.Show()

	// Avoid multiple CreateNasInstance requests for same NASInstanceIdentifier
	err = s.config.nasNameHolder.SetCreating(name)
	if err != nil {
		// Returning codes.Internal will let controller NOT to retry.
		err = status.Error(codes.Internal, err.Error())
		return
	}
	defer s.config.nasNameHolder.UnsetCreating(name)

	glog.V(5).Infof("Using capacity bytes %q for volume %q", capBytes, name)

	nasInput, err := GenerateNasInstanceInput(name, capBytes, req.GetParameters())
	if err != nil {
		err = status.Error(codes.InvalidArgument, err.Error())
		return
	}
	if pstr(nasInput.NetworkId) == "net-COMMON_PRIVATE" {
		// Common private networkId must set nil for API
		nasInput.NetworkId = nil
	}

	securityGroupName, err := getSecurityGroupName(ctx, s.config.driver)
	if err != nil {
		err = status.Error(codes.InvalidArgument, err.Error())
		return
	}
	nasInput.NASSecurityGroups = []string{securityGroupName}

	// Check volume source for snapshots
	snapshotID := ""
	volumeSource := req.GetVolumeContentSource()
	if volumeSource != nil {
		if _, ok := volumeSource.GetType().(*csi.VolumeContentSource_Snapshot); !ok {
			return nil, status.Error(codes.InvalidArgument, "Unsupported volumeContentSource type")
		}
		sourceSnapshot := volumeSource.GetSnapshot()
		if sourceSnapshot == nil {
			return nil, status.Error(codes.InvalidArgument, "Error retrieving snapshot from the volumeContentSource")
		}
		snapshotID = sourceSnapshot.GetSnapshotId()
	}

	// get annotations from PVC
	var reservedIPv4CIDR, permission string
	reservedIPv4CIDR, permission, err = getAnnotationsFromPVC(ctx, req.GetName(), s.config.driver)
	if err != nil {
		err = status.Error(codes.InvalidArgument, err.Error())
		return
	}

	// Check if the instance already exists
	n, err := s.config.cloud.GetNasInstance(ctx, name)
	if err != nil && !cloud.IsNotFoundErr(err) {
		err = status.Error(codes.Internal, err.Error())
		return
	}
	if n != nil {
		// Instance already exists, check if it meets the request
		if err = compareNasInstanceWithInput(n, nasInput); err != nil {
			err = status.Error(codes.AlreadyExists, err.Error())
			return
		}

	} else {

		if nasInput.NetworkId != nil {

			// If we are creating a new instance, we need pick an unused ip from reserved-ipv4-cidr
			if reservedIPv4CIDR == "" {
				reservedIPv4CIDR = req.GetParameters()["reservedIpv4Cidr"]
				if reservedIPv4CIDR == "" {
					err = status.Error(codes.InvalidArgument, "reservedIpv4Cidr must be provided")
					return
				}
				glog.V(4).Infof("Using reserved IPv4 CIDR of storage class : %s", reservedIPv4CIDR)
			} else {
				glog.V(4).Infof("Using reserved IPv4 CIDR of PVC : %s", reservedIPv4CIDR)
			}
			var reservedIP string
			reservedIP, err = s.reserveIP(ctx, reservedIPv4CIDR)

			// Possible cases are 1) CreateInstanceAborted, 2)CreateInstance running in background
			// The ListInstances response will contain the reservedIPs if the operation was started
			// In case of abort, the IP is released and available for reservation
			defer s.config.ipAllocator.ReleaseIP(reservedIP)
			if err != nil {
				return
			}
			reservedIP = reservedIP + "/24"

			// Adding the reserved IP to the instance input
			nasInput.MasterPrivateAddress = &reservedIP
		}

		// Create the instance
		glog.V(4).Infof("Create NAS Instance called with input %v", nasInput)
		n, err = s.config.cloud.CreateNasInstance(ctx, nasInput)
		if err != nil {
			err = status.Error(codes.Internal, err.Error())
			return
		}
	}

	if *n.NASInstanceStatus != "available" {
		if *n.NASInstanceStatus == "creating" || *n.NASInstanceStatus == "modifying" {
			// Wait for creating -> available
			err = s.waitForNASInstanceAvailable(name)
			if err != nil {
				err = status.Error(codes.Internal, "Error waiting for NASInstance creating > available: "+err.Error())
				return
			}
		} else {
			// Returning codes.Internal will let controller NOT to retry.
			err = status.Error(codes.Internal, fmt.Sprintf("NAS Instance %s status:%s", name, *n.NASInstanceStatus))
			return
		}
		// Must update n again
		n, err = s.config.cloud.GetNasInstance(context.TODO(), name)
		if err != nil {
			err = status.Error(codes.Internal, err.Error())
			return
		}
	}

	if *n.NoRootSquash == "false" {
		// Set no_root_squash and get NAS Instance again
		_, err = s.config.cloud.ModifyNasInstance(context.TODO(), name)
		if err != nil {
			err = status.Error(codes.Internal, err.Error())
			return
		}

		// Wait for modifying -> available again
		err = s.waitForNASInstanceAvailable(name)
		if err != nil {
			err = status.Error(codes.Internal, "Error waiting for NASInstance modifying > available: "+err.Error())
			return
		}

		// Must update n again
		n, err = s.config.cloud.GetNasInstance(context.TODO(), name)
		if err != nil {
			err = status.Error(codes.Internal, err.Error())
			return
		}
	}

	// Make source path for shared NAS instance
	if req.GetParameters()["shared"] == "true" {
		err = makeSourcePath(
			ctx, getNasInstancePrivateIP(n), *n.NASInstanceIdentifier, req.GetName(), permission, s.config.driver)
		if err != nil {
			err = status.Error(codes.Internal, "error making source path for shared NASInstance:"+err.Error())
			return
		}
	} else if permission != "" {
		err = changeModeSourcePath(
			ctx, getNasInstancePrivateIP(n), *n.NASInstanceIdentifier, "", permission, s.config.driver)
		if err != nil {
			err = status.Error(codes.Internal, "error changing source path mode for NASInstance:"+err.Error())
			return
		}
	}

	vol := s.nasInstanceToCSIVolume(n, req)
	glog.V(4).Infof("Volume created: %v", vol)

	// Restoring from snapshot
	if snapshotID != "" {
		err = s.restoreSnapshot(ctx, snapshotID, vol)
		if err != nil {
			err = status.Error(codes.Internal, "error restoring volume contents from snapshot"+err.Error())
			return
		}
		vol.ContentSource = &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Snapshot{
				Snapshot: &csi.VolumeContentSource_SnapshotSource{
					SnapshotId: snapshotID,
				},
			},
		}
		glog.V(4).Infof("Volume successfully restored from snapshot %s", snapshotID)
	}

	res = &csi.CreateVolumeResponse{Volume: vol}
	err = nil
	return
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
	// Initialize an empty reserved list. It will be populated with
	// all the reservedIPRanges obtained from the cloud instances
	cloudInstancesReservedIPs := make(map[string]bool)
	for _, instance := range instances {
		cloudInstancesReservedIPs[*instance.Endpoint.PrivateAddress] = true
	}
	return cloudInstancesReservedIPs, nil
}

// DeleteVolume deletes a GCFS instance
func (s *controllerServer) DeleteVolume(
	ctx context.Context, req *csi.DeleteVolumeRequest) (res *csi.DeleteVolumeResponse, err error) {
	glog.V(4).Infof("DeleteVolume called with request %v", *req)

	volumeID := req.GetVolumeId()
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is empty")
	}

	// Is shared
	shared := false
	sourcePath := ""
	tokens := strings.Split(volumeID, "/")
	if len(tokens) == 3 {
		volumeID = strings.Join(tokens[0:2], "/")
		sourcePath = tokens[2]
		shared = true
	}

	nas, err := s.config.cloud.GetNasInstanceFromVolumeID(ctx, volumeID)
	if err != nil {
		// An invalid ID should be treated as doesn't exist
		glog.V(5).Infof("failed to get instance for volume %v deletion: %v", volumeID, err)
		return &csi.DeleteVolumeResponse{}, nil
	}

	if shared {
		// Queue in delete PVs operaions
		defer func() {
			if err != nil {
				s.config.deletingPvsQueue.UnsetQueue(sourcePath)
			}
		}()
		err = s.config.deletingPvsQueue.Queue(sourcePath)
		if err != nil {
			return nil, status.Error(codes.Internal, "deleting queue:"+err.Error())
		}
		s.config.deletingPvsQueue.Show()
		// Remove source path on shared nas
		err := removeSourcePath(ctx, getNasInstancePrivateIP(nas), *nas.NASInstanceIdentifier, sourcePath, s.config.driver)
		if err != nil {
			return nil, status.Error(codes.Internal, "removing source path:"+err.Error())
		}
		// check any other PVs on shared NAS
		mustDeleted, err := noOtherPvsInSharedNas(ctx, sourcePath, *nas.NASInstanceIdentifier, s.config.driver)
		if err != nil {
			return nil, status.Error(codes.Internal, "checking other PVs on shared NAS:"+err.Error())
		}
		if !mustDeleted {
			return &csi.DeleteVolumeResponse{}, nil
		}
		glog.V(4).Infof("No other PVs than %s in shared NAS %s", sourcePath, *nas.NASInstanceIdentifier)
	}

	glog.V(4).Infof("Deleting NAS instance %s", *nas.NASInstanceIdentifier)
	err = s.config.cloud.DeleteNasInstance(ctx, *nas.NASInstanceIdentifier)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.DeleteVolumeResponse{}, nil
}

func (s *controllerServer) ValidateVolumeCapabilities(
	ctx context.Context,
	req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	// Validate arguments
	volumeID := req.GetVolumeId()
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is empty")
	}
	caps := req.GetVolumeCapabilities()
	if len(caps) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities is empty")
	}

	// Check that the volume exists
	_, err := s.config.cloud.GetNasInstanceFromVolumeID(ctx, volumeID)
	if err != nil {
		// An invalid id format is treated as doesn't exist
		return nil, status.Error(codes.NotFound, err.Error())
	}

	// Validate that the volume matches the capabilities
	// Note that there is nothing in the instance that we actually need to validate
	if err := s.config.driver.validateVolumeCapabilities(caps); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{
			//Supported: false,
			Message: err.Error(),
		}, status.Error(codes.InvalidArgument, err.Error())
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		//Supported: true,
	}, nil
}

func (s *controllerServer) ControllerGetCapabilities(
	ctx context.Context,
	req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: s.config.driver.cscap,
	}, nil
}

// getRequestCapacity returns the volume size that should be provisioned
func getRequestCapacity(capRange *csi.CapacityRange, min int64) int64 {
	if capRange == nil {
		return min
	}

	rCap := capRange.GetRequiredBytes()
	lCap := capRange.GetLimitBytes()

	if lCap > 0 {
		if rCap == 0 {
			// request not set
			return lCap
		}
		// request set, round up to min
		return util.Min(util.Max(rCap, min), lCap)
	}

	// limit not set
	return util.Max(rCap, min)
}

// getRequestCapacity returns the volume size that should be provisioned
func getRequestCapacityNoMinimum(capRange *csi.CapacityRange) int64 {
	return getRequestCapacity(capRange, 0)
}

func getNasInstancePrivateIP(n *nas.NASInstance) string {
	ip := "0.0.0.0"
	if n.Endpoint != nil && n.Endpoint.PrivateAddress != nil {
		ip = *n.Endpoint.PrivateAddress
	}
	return ip
}

func getNasInstanceCapacityBytes(n *nas.NASInstance) int64 {
	capacityGBytes, err := strconv.ParseInt(*n.AllocatedStorage, 10, 64)
	if err != nil {
		return 0
	}
	return util.GbToBytes(capacityGBytes)
}

// Generates a CSI volume spec from the shared cloud Instance and request
func (s *controllerServer) nasInstanceToCSIVolume(n *nas.NASInstance, req *csi.CreateVolumeRequest) *csi.Volume {
	ip := getNasInstancePrivateIP(n)
	if req.GetParameters()["shared"] == "true" {
		return &csi.Volume{
			VolumeId:      s.config.cloud.GenerateVolumeIDFromNasInstance(n) + "/" + req.GetName(),
			CapacityBytes: getRequestCapacityNoMinimum(req.GetCapacityRange()),
			VolumeContext: map[string]string{
				attrIP:         ip,
				attrSourcePath: req.GetName(),
			},
		}
	}
	return &csi.Volume{
		VolumeId:      s.config.cloud.GenerateVolumeIDFromNasInstance(n),
		CapacityBytes: getNasInstanceCapacityBytes(n),
		VolumeContext: map[string]string{
			attrIP:         ip,
			attrSourcePath: "",
		},
	}
}

///// Not implemented methods

func (s *controllerServer) ControllerPublishVolume(
	ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerPublishVolume unsupported")
}

func (s *controllerServer) ControllerUnpublishVolume(
	ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerUnpublishVolume unsupported")
}

func (s *controllerServer) ListVolumes(
	ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	// https://cloud.google.com/compute/docs/reference/beta/disks/list
	// List volumes in the whole region? In only the zone that this controller is running?
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *controllerServer) GetCapacity(
	ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	// https://cloud.google.com/compute/quotas
	// DISKS_TOTAL_GB.
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *controllerServer) ControllerExpandVolume(
	ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerExpandVolume unsupported")
}

func (s *controllerServer) ControllerGetVolume(
	ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerGetVolume unsupported")
}
