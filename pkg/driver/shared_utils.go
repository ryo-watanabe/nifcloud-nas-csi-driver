
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
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/util/mount"
	"k8s.io/apimachinery/pkg/util/rand"

	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/util"
)

// Convert CreateVolumeRequest into shared
func (s *controllerServer) convertSharedRequest(
	ctx context.Context,
	name string,
	req *csi.CreateVolumeRequest,
	) (string, int64, error) {

	if req.GetParameters()["shared"] != "true" {
		return name, getRequestCapacity(req.GetCapacityRange(), minVolumeSize), nil
	}

	err := s.config.creatingPvsQueue.Queue(name)
	if err != nil {
		return "", 0, fmt.Errorf("Error creating queue: %s", err.Error())
	}

	glog.V(4).Infof("Start creating shared PV %s", name)

	capBytes := getRequestCapacityNoMinimum(req.GetCapacityRange())
	sharedCapGiBytes, err := strconv.ParseInt(req.GetParameters()["capacityParInstanceGiB"], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("Invalid value in capacityParInstanceGiB: %s", err.Error())
	}
	sharedCapBytes := util.GbToBytes(sharedCapGiBytes)

	// TODO: must be check with sum of all shared PVs capacities
	if capBytes > sharedCapBytes {
		return "", 0, fmt.Errorf("Request capacity %d is too big to share a NAS instance.", capBytes)
	}

	// Get kube-system UID for cluster ID
	clusterUID, err := getNamespaceUID("kube-system", s.config.driver)
	if err != nil {
		return "", 0, fmt.Errorf("Error getting namespace UID: %S", err.Error())
	}

	// Search which NAS Instance the volume to settle in.
	sharedName := ""
	sharedNamePrefix := "cluster-" + clusterUID + "-shd" +  req.GetParameters()["instanceType"]
	for i := 1; i <= 10; i++ {
		// Generate NAS Instance name
		sharedName = sharedNamePrefix + fmt.Sprintf("%03d", i)
		reservedBytes, err := getNasInstanceReservedCap(sharedName, s.config.driver)
		if err != nil {
			return "", 0, fmt.Errorf("Error getting reserved cap of shared nas: %S", err.Error())
		}
		if reservedBytes == 0 {
			// New shared nas
			// Search creating NAS with shared name
			var ok bool
			sharedName, ok, err = getSharedNameFromExistingNas(ctx, sharedName, s.config.driver)
			if err != nil {
				return "", 0, fmt.Errorf("Error getting existing nas names: %s", err.Error())
			}
			if !ok {
				// Add random string at the end of nas name to be treated as another instance when recreated
				sharedName += "-" + rand.String(5)
			}
			glog.V(4).Infof("New shared NAS instance %s for %s", sharedName, name)
			break
		}
		sharedName, err = getSharedNameFromExistingPv(sharedName, s.config.driver)
		if err != nil {
			return "", 0, fmt.Errorf("Error getting shared nas name: %S", err.Error())
		}

		// Check available caps
		nas, err := s.config.driver.config.Cloud.GetNasInstance(ctx, sharedName)
		if err != nil {
			return "", 0, fmt.Errorf("Error getting nas instance: %S", err.Error())
		}
		if capBytes <= getNasInstanceCapacityBytes(nas) - reservedBytes {
			// Place this volume in this nas.
			glog.V(4).Infof("Place %s in shared NAS instance %s", name, sharedName)
			break
		}
		glog.V(4).Infof("No enough space for %s in shared NAS instance %s", name, sharedName)
		if i == 10 {
			return "", 0, fmt.Errorf("Cannot create more than 10 shared NASs")
		}
	}

	return sharedName, sharedCapBytes, nil
}

// Check available space in shared nas
// Both nas names with or without random string are OK for input nasName
func getNasInstanceReservedCap(nasName string, driver *NifcloudNasDriver) (int64, error) {
	kubeClient := driver.config.KubeClient

	// Get PV list
	pvs, err := kubeClient.CoreV1().PersistentVolumes().List(metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("Error getting PV list : %s", err.Error())
	}

	// Sum PV capacities in the NASInstance
	var used int64
	used = 0
	for _, pv := range pvs.Items {
		if csi := pv.Spec.PersistentVolumeSource.CSI; csi != nil {
			if strings.Contains(csi.VolumeHandle, nasName) {
				if capQuantity, ok := pv.Spec.Capacity["storage"]; ok {
					if capBytes, ok := capQuantity.AsInt64(); ok {
						glog.V(4).Infof("Reserved cap %d for %s", capBytes, pv.ObjectMeta.GetName())
						used += capBytes
					}
				}
			}
		}
	}

	glog.V(4).Infof("Total reserved cap %d in %s", used, nasName)
	return used, nil
}

// Check any other PVs on shared NAS
func noOtherPvsInSharedNas(name, nasName string, driver *NifcloudNasDriver) (bool, error) {
	kubeClient := driver.config.KubeClient

	// Get PV list
	pvs, err := kubeClient.CoreV1().PersistentVolumes().List(metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("Error getting PV list : %s", err.Error())
	}

	// Check other PV in the nas
	var nasFound = false
	var noOtherPVs = true
	for _, pv := range pvs.Items {
		if csi := pv.Spec.PersistentVolumeSource.CSI; csi != nil {
			if strings.Contains(csi.VolumeHandle, nasName) {
				nasFound = true
				if name != pv.ObjectMeta.GetName() {
					glog.V(4).Infof("Other PV %s found (status.phase=%s) in shared NAS %s", pv.ObjectMeta.GetName(), pv.Status.Phase, nasName)
					noOtherPVs = false
				}
			}
		}
	}
	if !nasFound {
		return false, fmt.Errorf("Nas %s not found in PV list", nasName)
	}

	return noOtherPVs, nil
}

// Get the nas name with rondom string from prefixed part of the name
func getSharedNameFromExistingPv(nasName string, driver *NifcloudNasDriver) (string, error) {
	kubeClient := driver.config.KubeClient

	// Get PV list
	pvs, err := kubeClient.CoreV1().PersistentVolumes().List(metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("Error getting PV list : %s", err.Error())
	}

	// Get csi.Volumehandle from PV
	for _, pv := range pvs.Items {
		if csi := pv.Spec.PersistentVolumeSource.CSI; csi != nil {
			if strings.Contains(csi.VolumeHandle, nasName) {
				// Parse VolumeHandle and get nas name
				tokens := strings.Split(csi.VolumeHandle, "/")
				if len(tokens) != 3 {
					return "", fmt.Errorf("VolumeHandle format error : %s", csi.VolumeHandle)
				}
				return tokens[1], nil
			}
		}
	}

	return "", fmt.Errorf("Cannot find VolumeHandle %s", nasName)
}

// Get the nas name with rondom string from prefixed part of the name
func getSharedNameFromExistingNas(ctx context.Context, nasName string, driver *NifcloudNasDriver) (string, bool, error) {
	instances, err := driver.config.Cloud.ListNasInstances(ctx)
	if err != nil {
		return nasName, false, err
	}
	for _, n := range instances {
		if strings.Contains(*n.NASInstanceIdentifier, nasName) && *n.NASInstanceStatus != "deleting" {
			return *n.NASInstanceIdentifier, true, nil
		}
	}
	return nasName, false, nil
}

// Get namespace UID
func getNamespaceUID(name string, driver *NifcloudNasDriver) (string, error) {
	kubeClient := driver.config.KubeClient
	ns, err := kubeClient.CoreV1().Namespaces().Get(name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("Error getting namespace : %s", err.Error())
	}
	return string(ns.ObjectMeta.GetUID()), nil
}

// Mount and mkdir/rmdir for shared volumes. Container must be privileged
func opSourcePath(ip, nasName, sourcePath string, remove bool) error {

	// Mount point
	mountPoint := filepath.Join("tmp", sourcePath, "mnt")
	err := os.MkdirAll(mountPoint, 0755)
	if err != nil {
		return fmt.Errorf("Error making mount point %s: %s", mountPoint, err.Error())
	}
	// Mount
	mounter := mount.New("")
	source := fmt.Sprintf("%s:/", ip)
	fstype := "nfs"
	options := []string{}
	err = mounter.Mount(source, mountPoint, fstype, options)
	if err != nil {
		return fmt.Errorf("Mount error:", err.Error())
	}
	// Operation
	tmppath := filepath.Join(mountPoint, sourcePath)
	if remove {
		// Remove source path
		err = os.RemoveAll(tmppath)
		if err != nil {
			return fmt.Errorf("Error removing source path %s: %s", tmppath, err.Error())
		}
	} else {
		// Make source path
		err = os.MkdirAll(tmppath, 0755)
		if err != nil {
			return fmt.Errorf("Error making source path %s: %s", tmppath, err.Error())
		}
	}
	// Umount
	err = mounter.Unmount(mountPoint)
	if err != nil {
		return fmt.Errorf("Umount error:", err.Error())
	}
	// Remove mount point
	mountPointParent := filepath.Join("tmp", sourcePath)
	err = os.RemoveAll(mountPointParent)
	if err != nil {
		return fmt.Errorf("Error removing mount point %s: %s", mountPointParent, err.Error())
	}

	return nil
}

func makeSourcePath(ip, nasName, sourcePath string) error {
	glog.V(5).Infof("Making source path: %s %s %s", ip, nasName, sourcePath)
	return opSourcePath(ip, nasName, sourcePath, false)
}

func removeSourcePath(ip, nasName, sourcePath string) error {
	glog.V(5).Infof("Removing source path: %s %s %s", ip, nasName, sourcePath)
	return opSourcePath(ip, nasName, sourcePath, true)
}
