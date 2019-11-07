
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

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/util/mount"

	//"github.com/alice02/nifcloud-sdk-go-v2/service/nas"
	//"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/cloud"
	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/util"
)

// Convert CreateVolumeRequest into shared
func convertSharedRequest(
	ctx context.Context,
	name string,
	req *csi.CreateVolumeRequest,
	driver *NifcloudNasDriver,
	) (string, int64, error) {

	if req.GetParameters()["shared"] != "true" {
		return name, getRequestCapacity(req.GetCapacityRange(), minVolumeSize), nil
	}
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
	clusterUID, err := getNamespaceUID("kube-system", driver)
	if err != nil {
		return "", 0, fmt.Errorf("Error getting namespace UID: %S", err.Error())
	}

	// Search which NAS Instance the volume to settle in.
	sharedName := ""
	sharedNamePrefix := "cluster-" + clusterUID + "-shared-" +  req.GetParameters()["instanceType"]
	for i := 1; i <= 10; i++ {
		// Generate NAS Instance name
		sharedName = sharedNamePrefix + fmt.Sprintf("%03d", i)
		reservedBytes, err := getNasInstanceReservedCap(sharedName, driver)
		if err != nil {
			return "", 0, fmt.Errorf("Error getting reserved cap of shared nas: %S", err.Error())
		}
		if reservedBytes == 0 {
			// New shared nas
			glog.V(5).Infof("New shared NAS instance %s for %s", sharedName, name)
			break
		}

		// Check available caps
		nas, err := driver.config.Cloud.GetNasInstance(ctx, sharedName)
		if err != nil {
			return "", 0, fmt.Errorf("Error getting nas instance: %S", err.Error())
		}
		if capBytes <= getNasInstanceCapacityBytes(nas) - reservedBytes {
			// Place this volume in this nas.
			glog.V(5).Infof("Place %s in shared NAS instance %s", name, sharedName)
			break
		}
		glog.V(5).Infof("No enough space for %s in shared NAS instance %s", name, sharedName)
	}

	return sharedName, sharedCapBytes, nil
}

// Check available space in shared nas
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
						glog.V(5).Infof("Reserved cap %d for %s", capBytes, pv.ObjectMeta.GetName())
						used += capBytes
					}
				}
			}
		}
	}

	glog.V(5).Infof("Total reserved cap %d in %s", used, nasName)
	return used, nil
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
	mountPoint := filepath.Join("tmp", nasName)
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
