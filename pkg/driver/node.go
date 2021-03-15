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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	//csi "github.com/container-storage-interface/spec/lib/go/csi/v0"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/mount"
)

const (
	optionSmbUser     = "smbUser"
	optionSmbPassword = "smbPassword"
)

var (
	// For testing purposes
	goOs = runtime.GOOS
)

func registerNodePrivateIp(config *NifcloudNasDriverConfig) error {
	// Get private IP (if:ens192)
	privateIp, err := exec.Command("sh", "-c", "ip -4 a show "+config.PrivateIfName+" | grep inet | tr -s ' ' | cut -d' ' -f3").Output()
	if err != nil {
		return err
	}
	glog.Infof("Node private IP : %s", privateIp)

	// Annotate private ip in csinode resource
	ctx := context.TODO()
	csiNode, err := config.KubeClient.StorageV1().CSINodes().Get(ctx, config.NodeID, metav1.GetOptions{})
	if err != nil {
		return err
	}
	glog.Infof("CSINode spec : %v", csiNode.Spec)
	annotations := csiNode.ObjectMeta.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string, 0)
	}
	annotations[config.Name+"/privateIp"] = strings.Split(string(privateIp), "/")[0]
	csiNode.ObjectMeta.SetAnnotations(annotations)
	csiNode, err = config.KubeClient.StorageV1().CSINodes().Update(ctx, csiNode, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	return nil
}

// nodeServer handles mounting and unmounting of GCFS volumes on a node
type nodeServer struct {
	driver  *NifcloudNasDriver
	mounter mount.Interface
}

func newNodeServer(driver *NifcloudNasDriver, mounter mount.Interface) (csi.NodeServer, error) {
	return &nodeServer{
		driver:  driver,
		mounter: mounter,
	}, nil
}

// NodePublishVolume mounts the GCFS volume
func (s *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	// TODO: make this idempotent. Multiple requests for the same volume can come in parallel, this needs to be seralized
	// We need something like the nestedpendingoperations

	// Validate arguments
	readOnly := req.GetReadonly()
	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume target path must be provided")
	}

	if err := s.driver.validateVolumeCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Validate volume attributes
	attr := req.GetVolumeContext()
	if err := validateVolumeAttributes(attr); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Check if mount/targetPath already exists
	mounted, err := s.isDirMounted(targetPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// On Windows the mount target must not exist
		if goOs != "windows" {
			// Make target path
			err = os.MkdirAll(targetPath, 0755)
			if err != nil {
				return nil, fmt.Errorf("Error making target path %s: %s", targetPath, err.Error())
			}
		}
	}

	if mounted {
		// Already mounted
		// TODO: validate it's the corret mount
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Mount source
	source := fmt.Sprintf("%s:/%s", attr[attrIp], attr[attrSourcePath])

	// FileSystem type
	fstype := "nfs"

	// Mount options
	options := []string{}

	/*// Windows specific values
	if goOs == "windows" {
		source = fmt.Sprintf("\\\\%s\\%s", attr[attrIp], attr[attrVolume])
		fstype = "cifs"

		// Login credentials

		secrets := req.GetNodePublishSecrets()
		if err := validateSmbNodePublishSecrets(secrets); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}

		options = append(options, secrets[optionSmbUser])
		options = append(options, secrets[optionSmbPassword])
	}*/

	if readOnly {
		options = append(options, "ro")
	}
	if capMount := req.GetVolumeCapability().GetMount(); capMount != nil {
		options = append(options, capMount.GetMountFlags()...)
	}

	err = s.mounter.Mount(source, targetPath, fstype, options)
	if err != nil {
		glog.Errorf("Mount %q failed, cleaning up", targetPath)
		if unmntErr := s.unmountPath(targetPath); unmntErr != nil {
			glog.Errorf("Unmount %q failed: %v", targetPath, unmntErr)
		}
		return nil, status.Error(codes.Internal, fmt.Sprintf("mount %q failed: %v", targetPath, err))
	}

	glog.V(4).Infof("Successfully mounted %s", targetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unmounts the GCFS volume
func (s *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	// TODO: make this idempotent

	// Validate arguments
	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeUnpublishVolume target path must be provided")
	}

	if err := s.unmountPath(targetPath); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

/*
func (s *nodeServer) NodeGetId(ctx context.Context, req *csi.NodeGetIdRequest) (*csi.NodeGetIdResponse, error) {
	return &csi.NodeGetIdResponse{
		NodeId: s.driver.config.NodeID,
	}, nil
}
*/

func (s *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId:            s.driver.config.NodeID,
		MaxVolumesPerNode: 128,
	}, nil
}

func (s *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: s.driver.nscap,
	}, nil
}

// validateVolumeAttributes checks for all the necessary fields for mounting the volume
func validateVolumeAttributes(attr map[string]string) error {
	// TODO: validate ip syntax
	if attr[attrIp] == "" {
		return fmt.Errorf("volume attribute %v not set", attrIp)
	}
	// TODO: validate allowed characters
	if _, ok := attr[attrSourcePath]; !ok {
		return fmt.Errorf("volume attribute %v not set", attrSourcePath)
	}
	return nil
}

func validateSmbNodePublishSecrets(secrets map[string]string) error {
	if secrets[optionSmbUser] == "" {
		return fmt.Errorf("secret %v not set", optionSmbUser)
	}

	if secrets[optionSmbPassword] == "" {
		return fmt.Errorf("secret %v not set", optionSmbPassword)
	}
	return nil
}

// isDirMounted checks if the path is already a mount point
func (s *nodeServer) isDirMounted(targetPath string) (bool, error) {
	// Check if mount already exists
	// TODO(msau): check why in-tree uses IsNotMountPoint
	// something related to squash and not having permissions to lstat
	notMnt, err := s.mounter.IsLikelyNotMountPoint(targetPath)
	if err != nil {
		return false, err
	}
	if !notMnt {
		// Already mounted
		return true, nil
	}
	return false, nil
}

// unmountPath unmounts the given path if it a mount point
func (s *nodeServer) unmountPath(targetPath string) error {
	mounted, err := s.isDirMounted(targetPath)
	if os.IsNotExist(err) {
		// Volume already unmounted
		glog.V(4).Infof("Mount point %q already unmounted", targetPath)
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to check mount point %q: %v", targetPath, err)
	}

	if mounted {
		glog.V(4).Infof("Unmounting %q", targetPath)
		err := s.mounter.Unmount(targetPath)
		if err != nil {
			return fmt.Errorf("unmount %q failed: %v", targetPath, err)
		}
	}
	return nil
}

type volumeStats struct {
	availBytes int64
	totalBytes int64
	usedBytes  int64

	availInodes int64
	totalInodes int64
	usedInodes  int64
}

func getVolumeStats(volumePath string) (*volumeStats, error) {
	var statfs unix.Statfs_t
	err := unix.Statfs(volumePath, &statfs)
	if err != nil {
		return nil, err
	}

	volStats := &volumeStats{
		availBytes: int64(statfs.Bavail) * int64(statfs.Bsize),
		totalBytes: int64(statfs.Blocks) * int64(statfs.Bsize),
		usedBytes:  (int64(statfs.Blocks) - int64(statfs.Bfree)) * int64(statfs.Bsize),

		availInodes: int64(statfs.Ffree),
		totalInodes: int64(statfs.Files),
		usedInodes:  int64(statfs.Files) - int64(statfs.Ffree),
	}

	return volStats, nil
}

func (s *nodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {

	volumePath := req.GetVolumePath()
	if volumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "Error getting volume path")
	}

	stats, err := getVolumeStats(volumePath)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, status.Errorf(codes.NotFound, "volume path %v is not mounted", volumePath)
		}
		return nil, status.Errorf(codes.Internal, "failed to retrieve statistics for volume path %v: %v", volumePath, err)
	}

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			&csi.VolumeUsage{
				Available: stats.availBytes,
				Total:     stats.totalBytes,
				Used:      stats.usedBytes,
				Unit:      csi.VolumeUsage_BYTES,
			},
			&csi.VolumeUsage{
				Available: stats.availInodes,
				Total:     stats.totalInodes,
				Used:      stats.usedInodes,
				Unit:      csi.VolumeUsage_INODES,
			},
		},
	}, nil
}

//// Unimplemented funcs

func (s *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "NodeStageVolume unsupported")
}

func (s *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "NodeUnStageVolume unsupported")
}

func (s *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "NodeExpandVolume unsupported")
}
