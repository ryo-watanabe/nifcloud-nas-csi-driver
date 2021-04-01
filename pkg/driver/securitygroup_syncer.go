
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
	"reflect"

	"github.com/golang/glog"
	"golang.org/x/net/context"
	"k8s.io/apimachinery/pkg/util/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nifcloud/nifcloud-sdk-go/service/nas"
	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/cloud"
)

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

func (s *NSGSyncer) DoesHaveTask() bool {
	return s.hasTask
}

func getSecurityGroupName(ctx context.Context, driver *NifcloudNasDriver) (string, error) {
	// Get kube-system UID for NAS Security Group Name
	clusterUID, err := getNamespaceUID(ctx, "kube-system", driver)
	if err != nil {
		return "", fmt.Errorf("Error getting namespace UUID: %s", err.Error())
	}

	return "cluster-" + clusterUID, nil
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
	csinodes, err := kubeClient.StorageV1().CSINodes().List(ctx, metav1.ListOptions{})
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
	securityGroupName, err := getSecurityGroupName(ctx, s.driver)
	if err != nil {
		return fmt.Errorf("Error getting security group name: %s", err.Error())
	}
	classes, err := kubeClient.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("Error getting storage class: %s", err.Error())
	}
	securityGroupInputs := make(map[string]nas.CreateNASSecurityGroupInput, 0)
	for _, class := range classes.Items {
		if class.Provisioner == s.driver.config.Name {
			zone := class.Parameters["zone"]
			if securityGroupName != "" && zone != "" {
				securityGroupInputs[securityGroupName] = nas.CreateNASSecurityGroupInput{
					AvailabilityZone: &zone,
					NASSecurityGroupName: &securityGroupName,
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

		// Check security group is editable
		editable := true
		for _, aip := range nsg.IPRanges {
			if *aip.Status != "authorized" {
				editable = false
				break
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
				if !editable {
					// Not recovered from Client.Resource.IncorrectState.ApplyNASSecurityGroup
					glog.V(4).Infof("Pending authorize CIDRIP %s : SecurityGroup %s not in editable state", iprange, *scInput.NASSecurityGroupName)
					return nil
				}
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
				if !editable {
					// Not recovered from Client.Resource.IncorrectState.ApplyNASSecurityGroup
					glog.V(4).Infof("Pending revoke CIDRIP %s : SecurityGroup %s not in editable state", *aip.CIDRIP, *scInput.NASSecurityGroupName)
					return nil
				}
				_, err = s.driver.config.Cloud.RevokeCIDRIP(ctx, *scInput.NASSecurityGroupName, *aip.CIDRIP)
				if err != nil {
					return fmt.Errorf("Error revoking NASSecurityGroup ingress: %s", err.Error())
				}
				glog.V(4).Infof("CIDRIP %s revoked in SecurityGroup %s", *aip.CIDRIP, *scInput.NASSecurityGroupName)
				// Do one task only to wait recovery of Client.Resource.IncorrectState.ApplyNASSecurityGroup.
				return nil
			}
		}
	}

	// Tasks completed here
	s.hasTask = false

	return nil
}
