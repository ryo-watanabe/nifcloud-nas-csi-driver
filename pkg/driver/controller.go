
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
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"github.com/cenkalti/backoff"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	"github.com/aokumasan/nifcloud-sdk-go-v2/service/nas"
	"gitlab.devops.nifcloud.net/x_nke/hatoba-nas-csi-driver/pkg/cloud"
	"gitlab.devops.nifcloud.net/x_nke/hatoba-nas-csi-driver/pkg/util"
)

const (
	// premium tier min is 2.5 Tb, let GCFS error
	minVolumeSize     int64 = 100 * util.Gb
	//modeInstance            = "modeInstance"
	//newInstanceVolume       = "vol1"

	//defaultTier    = "standard"
	//defaultNetwork = "default"
	driverNamespace = "nifcloud-nas-csi-driver" // Must set in options !!!
)

// Volume attributes
const (
	attrIp         = "ip"
	attrSourcePath = "path"
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
	nasNameHolder *util.InstanceNameHolder
	volumeInformer cache.SharedInformer
	creatingPvsQueue *util.OperateResourceQueue
	deletingPvsQueue *util.OperateResourceQueue
}

func newControllerServer(config *controllerServerConfig) csi.ControllerServer {
	config.ipAllocator = util.NewIPAllocator(make(map[string]bool))
	config.nasNameHolder = util.NewInstanceNameHolder()
	config.creatingPvsQueue = util.NewOperateResourceQueue("Creating PVs queue")
	config.deletingPvsQueue = util.NewOperateResourceQueue("Deleting PVs queue")
	controller := &controllerServer{config: config}

	// Prepare PersistenVolume informer
	informer := informers.NewSharedInformerFactory(config.driver.config.KubeClient, time.Second*10)
	volumeHandler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { controller.pvAdded(obj) },
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
func (c *controllerServer) pvAdded(obj interface{}) {
	var key string
	var err error
	if key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	glog.V(4).Infof("PersistentVolume %s added", key)
	c.config.creatingPvsQueue.UnsetQueue(key)
}

// Called when PersistentVolume resource deleted
func (c *controllerServer) pvDeleted(obj interface{}) {
	var key string
	var err error
	if key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	glog.V(4).Infof("PersistentVolume %s deleted", key)
	c.config.deletingPvsQueue.UnsetQueue(key)
}

// Get reserved CIDRIP from PVC's annotations
func getIpv4CiderFromPVC(name string, driver *NifcloudNasDriver) (string, error) {
	kubeClient := driver.config.KubeClient
	pvcUIDs := strings.SplitN(name, "-", 2)
	if len(pvcUIDs) < 2 {
		return "", fmt.Errorf("Error getting IP from PVC : Cannot split UID")
	}
	pvcs, err := kubeClient.CoreV1().PersistentVolumeClaims("").List(metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("Error getting PVC list : %s", err.Error())
	}
	for _, pvc := range pvcs.Items {
		if string(pvc.ObjectMeta.GetUID()) == pvcUIDs[1] {
			if cidrip, ok := pvc.ObjectMeta.GetAnnotations()[driver.config.Name + "/reservedIPv4Cidr"]; ok {
				if !strings.Contains(cidrip, "/") {
					cidrip = cidrip + "/32"
				}
				return cidrip, nil
			}
			// PVC without annotation is acceptable
			return "", nil
		}
	}
	return "", fmt.Errorf("Error PVC %s not found", pvcUIDs[1])
}

func (s *controllerServer) waitForNASInstanceAvailable(name string) error {
	// wait for NAS available with backoff retry
	b := backoff.NewExponentialBackOff()
	b.MaxElapsedTime = time.Duration(30) * time.Minute
	b.RandomizationFactor = 0.2
	b.Multiplier = 1.0
	b.InitialInterval = 30 * time.Second
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

// CreateVolume creates a NAS instance
func (s *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (res *csi.CreateVolumeResponse, err error) {
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

	nasInput, err := cloud.GenerateNasInstanceInput(name, capBytes, req.GetParameters())
	if err != nil {
		err = status.Error(codes.InvalidArgument, err.Error())
		return
	}
	securityGroupName, err := getSecurityGroupName(s.config.driver)
	if err != nil {
		err = status.Error(codes.InvalidArgument, err.Error())
		return
	}
	nasInput.NASSecurityGroups = []string{securityGroupName}

	// Check if the instance already exists
	n, err := s.config.cloud.GetNasInstance(ctx, name)
	if err != nil && !cloud.IsNotFoundErr(err) {
		err = status.Error(codes.Internal, err.Error())
		return
	}
	if n != nil {
		// Instance already exists, check if it meets the request
		if err = cloud.CompareNasInstanceWithInput(n, nasInput); err != nil {
			err = status.Error(codes.AlreadyExists, err.Error())
			return
		}

	} else {
		// If we are creating a new instance, we need pick an unused ip from reserved-ipv4-cidr
		var reservedIPv4CIDR string
		reservedIPv4CIDR, err = getIpv4CiderFromPVC(req.GetName(), s.config.driver)
		if err != nil {
			err = status.Error(codes.InvalidArgument, "volume name invalid")
			return
		}
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
				err = status.Error(codes.Internal, "Error waiting for NASInstance creating > available: " + err.Error())
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
		n, err = s.config.cloud.ModifyNasInstance(context.TODO(), name)
		if err != nil {
			err = status.Error(codes.Internal, err.Error())
			return
		}

		// Wait for modifying -> available again
		err = s.waitForNASInstanceAvailable(name)
		if err != nil {
			err = status.Error(codes.Internal, "Error waiting for NASInstance modifying > available: " + err.Error())
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
		err = makeSourcePath(getNasInstancePrivateIP(n), *n.NASInstanceIdentifier, req.GetName())
		if err != nil {
			err = status.Error(codes.Internal, "error making source path for shared NASInstance:" + err.Error())
			return
		}
	}

	vol := s.nasInstanceToCSIVolume(n, req)
	glog.V(4).Infof("Volume created: %v", vol)

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
	// Initialize an empty reserved list. It will be populated with all the reservedIPRanges obtained from the cloud instances
	cloudInstancesReservedIPs := make(map[string]bool)
	for _, instance := range instances {
		cloudInstancesReservedIPs[*instance.Endpoint.PrivateAddress] = true
	}
	return cloudInstancesReservedIPs, nil
}

// DeleteVolume deletes a GCFS instance
func (s *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (res *csi.DeleteVolumeResponse, err error) {
	glog.V(4).Infof("DeleteVolume called with request %v", *req)

	volumeId := req.GetVolumeId()
	if volumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is empty")
	}

	// Is shared
	shared := false
	sourcePath := ""
	tokens := strings.Split(volumeId, "/")
	if len(tokens) == 3 {
		volumeId = strings.Join(tokens[0:2], "/")
		sourcePath = tokens[2]
		shared = true
	}

	nas, err := s.config.cloud.GetNasInstanceFromVolumeId(ctx, volumeId)
	if err != nil {
		// An invalid ID should be treated as doesn't exist
		glog.V(5).Infof("failed to get instance for volume %v deletion: %v", volumeId, err)
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
			return nil, status.Error(codes.Internal, "deleting queue:" + err.Error())
		}
		s.config.deletingPvsQueue.Show()
		// Remove source path on shared nas
		err := removeSourcePath(getNasInstancePrivateIP(nas), *nas.NASInstanceIdentifier, sourcePath)
		if err != nil {
			return nil, status.Error(codes.Internal, "removing source path:" + err.Error())
		}
		// check any other PVs on shared NAS
		mustDeleted, err := noOtherPvsInSharedNas(sourcePath, *nas.NASInstanceIdentifier, s.config.driver)
		if err != nil {
			return nil, status.Error(codes.Internal, "checking other PVs on shared NAS:" + err.Error())
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
		} else {
			// request set, round up to min
			return util.Min(util.Max(rCap, min), lCap)
		}
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
			VolumeId: s.config.cloud.GenerateVolumeIdFromNasInstance(n) + "/" + req.GetName(),
			CapacityBytes: getRequestCapacityNoMinimum(req.GetCapacityRange()),
			VolumeContext: map[string]string{
				attrIp: ip,
				attrSourcePath: req.GetName(),
			},
		}
	} else {
		return &csi.Volume{
			VolumeId: s.config.cloud.GenerateVolumeIdFromNasInstance(n),
			CapacityBytes: getNasInstanceCapacityBytes(n),
			VolumeContext: map[string]string{
				attrIp: ip,
				attrSourcePath: "",
			},
		}

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
