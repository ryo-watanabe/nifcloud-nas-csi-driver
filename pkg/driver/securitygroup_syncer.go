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
	"reflect"
	"strings"
	"time"

	"github.com/golang/glog"
	"golang.org/x/net/context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"

	"github.com/nifcloud/nifcloud-sdk-go/service/nas"
	"github.com/ryo-watanabe/nifcloud-nas-csi-driver/pkg/cloud"
)

// NSGSyncer provides nodes private IPs and NASSecurityGroup authorized IPs synchronization
type NSGSyncer struct {
	driver                 *NifcloudNasDriver
	SyncPeriod             int64
	internalChkIntvl       int64
	cloudChkIntvl          int64
	hasTask                bool
	restoreClusterID       bool
	lastNodePrivateIps     *map[string]bool
	lastSecurityGroupInput *nas.CreateNASSecurityGroupInput
}

func newNSGSyncer(driver *NifcloudNasDriver) *NSGSyncer {
	return &NSGSyncer{
		driver:                 driver,
		SyncPeriod:             30,   // seconds
		internalChkIntvl:       300,  // seconds
		cloudChkIntvl:          3600, // seconds
		hasTask:                true,
		restoreClusterID:       driver.config.RestoreClstID,
		lastNodePrivateIps:     nil,
		lastSecurityGroupInput: nil,
	}
}

func (s *NSGSyncer) runNSGSyncer() {
	err := s.SyncNasSecurityGroups()
	if err != nil {
		runtime.HandleError(err)
	}
}

// DoesHaveTask returns hasTask
func (s *NSGSyncer) DoesHaveTask() bool {
	return s.hasTask
}

func getSecurityGroupName(ctx context.Context, driver *NifcloudNasDriver) (string, error) {
	// Get kube-system UID for NAS Security Group Name
	clusterUID, err := getClusterUID(ctx, driver)
	if err != nil {
		return "", fmt.Errorf("Error getting namespace UUID: %s", err.Error())
	}

	return "cluster-" + clusterUID, nil
}

// SyncNasSecurityGroups syncs nodes private IPs and NASSecurityGroup authorized IPs
func (s *NSGSyncer) SyncNasSecurityGroups() error {

	// Skip some syncs when current tasks seem to be done.
	p := time.Now().Unix() / s.SyncPeriod
	doInternalChk := (p%(s.internalChkIntvl/s.SyncPeriod) == 0)
	doCloudChk := (p%(s.cloudChkIntvl/s.SyncPeriod) == 0)
	glog.V(5).Infof("SyncNasSecurityGroups %d internal check:%v cloud check:%v", p, doInternalChk, doCloudChk)

	if !s.hasTask && !doInternalChk && !doCloudChk {
		return nil
	}

	glog.V(5).Infof("SyncNasSecurityGroups internal check")

	kubeClient := s.driver.config.KubeClient
	ctx := context.TODO()

	// Nodes' private IPs
	csinodes, err := kubeClient.StorageV1().CSINodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("Error getting node private IPs: %s", err.Error())
	}
	nodePrivateIps := make(map[string]bool)
	for _, node := range csinodes.Items {
		annotations := node.ObjectMeta.GetAnnotations()
		if annotations != nil {
			privateIP := annotations[s.driver.config.Name+"/privateIp"]
			if privateIP != "" {
				nodePrivateIps[privateIP] = true
			}
		}
	}
	glog.V(5).Infof("nodePrivateIps : %v", nodePrivateIps)

	// NAS Security Groups
	securityGroupName, err := getSecurityGroupName(ctx, s.driver)
	if err != nil {
		return fmt.Errorf("Error getting security group name: %s", err.Error())
	}
	classes, err := kubeClient.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("Error getting storage class: %s", err.Error())
	}
	var securityGroupInput *nas.CreateNASSecurityGroupInput
	for _, class := range classes.Items {
		if class.Provisioner == s.driver.config.Name {
			zone := class.Parameters["zone"]
			if securityGroupName != "" && zone != "" {
				securityGroupInput = &nas.CreateNASSecurityGroupInput{
					AvailabilityZone:     &zone,
					NASSecurityGroupName: &securityGroupName,
				}
				break
			}
		}
	}

	if securityGroupInput == nil {
		return fmt.Errorf("valid zone or driver name %s not found in StorageClass", s.driver.config.Name)
	}
	glog.V(5).Infof("securityGroupInput : %v", securityGroupInput)

	// Internal check
	if s.lastNodePrivateIps != nil && s.lastSecurityGroupInput != nil {
		if reflect.DeepEqual(nodePrivateIps, *s.lastNodePrivateIps) &&
			reflect.DeepEqual(*securityGroupInput, *s.lastSecurityGroupInput) {
			if !s.hasTask && !doCloudChk {
				return nil
			}
		} else {
			// Nodes or storage classes changed.
			glog.V(4).Infof("SyncNasSecurityGroups nodes or storage classes changed")
			s.lastNodePrivateIps = &nodePrivateIps
			s.lastSecurityGroupInput = securityGroupInput
			s.hasTask = true
		}
	} else {
		// Nodes or storage classes changed.
		s.lastNodePrivateIps = &nodePrivateIps
		s.lastSecurityGroupInput = securityGroupInput
		s.hasTask = true
	}

	glog.V(5).Infof("SyncNasSecurityGroups cloud check")

	// Check cloud and synchronize

	// Check if the NAS Security Group exists and create one if not.
	nsg, err := s.driver.config.Cloud.GetNasSecurityGroup(ctx, pstr(securityGroupInput.NASSecurityGroupName))
	if err != nil {
		if cloud.IsNotFoundErr(err) {
			nsg, err = s.driver.config.Cloud.CreateNasSecurityGroup(ctx, securityGroupInput)
			if err != nil {
				return fmt.Errorf("Error creating NASSecurityGroup: %s", err.Error())
			}
			glog.V(4).Infof("NAS SecurityGroup %s created", pstr(securityGroupInput.NASSecurityGroupName))
		} else {
			return fmt.Errorf("Error getting NASSecurityGroup: %s", err.Error())
		}
	}

	// Restore NASSecurityGroup name on starting if there are PVs from snapshot with the old cluster's NASSecurityGroup
	if s.restoreClusterID {
		// Try restoring only one time
		s.restoreClusterID = false

		// Get nas names from PVs
		pvs, err := kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("Error PV list: %s", err.Error())
		}
		volumeIDs := map[string]bool{}
		for _, pv := range pvs.Items {
			if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == s.driver.config.Name {
				tokens := strings.Split(pv.Spec.CSI.VolumeHandle, "/")
				if len(tokens) < 2 {
					return fmt.Errorf("VolumeHandle format error : %s", pv.Spec.CSI.VolumeHandle)
				}
				volumeID := strings.Join(tokens[0:2], "/")
				volumeIDs[volumeID] = true
			}
		}

		// Check and repair NASSecurityGroup Name
		cnt := 0
		for volumeID := range volumeIDs {
			nas, err := s.driver.config.Cloud.GetNasInstanceFromVolumeID(ctx, volumeID)
			if err != nil {
				return fmt.Errorf("Error getting NASInstance %s : %s", volumeID, err.Error())
			}
			if len(nas.NASSecurityGroups) == 0 {
				return fmt.Errorf("Error getting no NASSecurityGroups in NASInstance %s", volumeID)
			}
			if pstr(nas.NASSecurityGroups[0].NASSecurityGroupName) == securityGroupName {
				glog.V(4).Infof("Found NAS %s in the cluster's NASSecurityGroup", volumeID)
				cnt++
			} else {
				glog.V(4).Infof("Found NAS %s in the other cluster's NASSecurityGroup %s ... Repairing",
					volumeID, pstr(nas.NASSecurityGroups[0].NASSecurityGroupName))
				// Repair
				_, err := s.driver.config.Cloud.ChangeNasInstanceSecurityGroup(
					ctx,
					pstr(nas.NASInstanceIdentifier),
					securityGroupName,
				)
				if err != nil {
					return fmt.Errorf("Error setting NASSecurityGroup %s to %s : %s",
						pstr(nas.NASInstanceIdentifier),
						securityGroupName,
						err.Error(),
					)
				}
			}
		}
	}

	// Check security group is editable
	editable := true
	for _, aip := range nsg.IPRanges {
		if *aip.Status != "authorized" {
			editable = false
			break
		}
	}

	// Authorize node private ip if not.
	for ip := range nodePrivateIps {
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
			if !editable {
				// Not recovered from Client.Resource.IncorrectState.ApplyNASSecurityGroup
				glog.V(4).Infof("Pending authorize CIDRIP %s : SecurityGroup %s not in editable state",
					iprange, pstr(securityGroupInput.NASSecurityGroupName))
				return nil
			}
			_, err = s.driver.config.Cloud.AuthorizeCIDRIP(ctx, pstr(securityGroupInput.NASSecurityGroupName), iprange)
			if err != nil {
				return fmt.Errorf("Error authorizing NASSecurityGroup ingress: %s", err.Error())
			}
			glog.V(4).Infof("CIDRIP %s authorized in SecurityGroup %s", iprange, pstr(securityGroupInput.NASSecurityGroupName))
			// Do one task only to wait recovery of Client.Resource.IncorrectState.ApplyNASSecurityGroup.
			return nil
		}
	}

	// Revoke CIDRIPs not in node private IPs.
	for _, aip := range nsg.IPRanges {
		nodeExists := false
		for ip := range nodePrivateIps {
			iprange := ip + "/32"
			if *aip.CIDRIP == iprange {
				nodeExists = true
				break
			}
		}
		if !nodeExists {
			s.hasTask = true
			if !editable {
				// Not recovered from Client.Resource.IncorrectState.ApplyNASSecurityGroup
				glog.V(4).Infof("Pending revoke CIDRIP %s : SecurityGroup %s not in editable state",
					pstr(aip.CIDRIP), pstr(securityGroupInput.NASSecurityGroupName))
				return nil
			}
			_, err = s.driver.config.Cloud.RevokeCIDRIP(
				ctx, pstr(securityGroupInput.NASSecurityGroupName), pstr(aip.CIDRIP))
			if err != nil {
				return fmt.Errorf("Error revoking NASSecurityGroup ingress: %s", err.Error())
			}
			glog.V(4).Infof("CIDRIP %s revoked in SecurityGroup %s",
				pstr(aip.CIDRIP), pstr(securityGroupInput.NASSecurityGroupName))

			// Do one task only to wait recovery of Client.Resource.IncorrectState.ApplyNASSecurityGroup.
			return nil
		}
	}

	// Tasks completed here
	s.hasTask = false

	return nil
}
