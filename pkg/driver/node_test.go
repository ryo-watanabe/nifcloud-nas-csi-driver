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
	"flag"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
	"k8s.io/utils/mount"
	"k8s.io/apimachinery/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

var (
	testVolumeCapability = &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{},
		},
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
	}
	testVolumeAttributes = map[string]string{
		attrIp:     "1.1.1.1",
		attrSourcePath: "test-volume",
	}
	testDevice = "1.1.1.1:/test-volume"
	testVolumeID = "testregion/pvc-TESTPVCID"
)

type nodeServerTestEnv struct {
	ns csi.NodeServer
	fm *mount.FakeMounter
}

func initTestNodeServer(t *testing.T) *nodeServerTestEnv {
	// test k8s
	kubeobjects := []runtime.Object{}
	kubeClient := k8sfake.NewSimpleClientset(kubeobjects...)
	// TODO: make a constructor in FakeMmounter library
	mounter := &mount.FakeMounter{MountPoints: []mount.MountPoint{}}
	return &nodeServerTestEnv{
		ns: newNodeServer(initTestDriver(t, nil, kubeClient, false, true), mounter),
		fm: mounter,
	}
}

func TestNodePublishVolume(t *testing.T) {
	defaultPerm := os.FileMode(0750) + os.ModeDir

	// Setup mount target path
	base, err := ioutil.TempDir("", "node-publish-")
	if err != nil {
		t.Fatalf("failed to setup testdir: %v", err)
	}
	testTargetPath := filepath.Join(base, "mount")
	testTargetPathAlreadyExists := filepath.Join(base, "mount-exists")
	if err = os.MkdirAll(testTargetPathAlreadyExists, defaultPerm); err != nil {
		t.Fatalf("failed to setup target path: %v", err)
	}
	defer os.RemoveAll(base)

	cases := []struct {
		name          string
		mounts        []mount.MountPoint // already existing mounts
		req           *csi.NodePublishVolumeRequest
		actions       []mount.FakeAction
		expectedMount *mount.MountPoint
		expectErr     bool
	}{
		{
			name:      "empty request",
			req:       &csi.NodePublishVolumeRequest{},
			expectErr: true,
		},
		{
			name: "valid request",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    testVolumeAttributes,
			},
			actions:       []mount.FakeAction{{Action: mount.FakeActionMount}},
			expectedMount: &mount.MountPoint{Device: testDevice, Path: testTargetPath, Type: "nfs"},
		},
		{
			name: "valid request path already exists",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPathAlreadyExists,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    testVolumeAttributes,
			},
			actions:       []mount.FakeAction{{Action: mount.FakeActionMount}},
			expectedMount: &mount.MountPoint{Device: testDevice, Path: testTargetPathAlreadyExists, Type: "nfs"},
		},
		{
			name:   "valid request already mounted",
			mounts: []mount.MountPoint{{Device: "/test-device", Path: testTargetPathAlreadyExists}},
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPathAlreadyExists,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    testVolumeAttributes,
			},
			expectedMount: &mount.MountPoint{Device: "/test-device", Path: testTargetPathAlreadyExists},
		},
		{
			name: "valid request with user mount options",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{
							MountFlags: []string{"foo", "bar"},
						},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
				VolumeContext:    testVolumeAttributes,
			},
			actions: []mount.FakeAction{{Action: mount.FakeActionMount}},

			expectedMount: &mount.MountPoint{Device: testDevice, Path: testTargetPath, Type: "nfs", Opts: []string{"foo", "bar"}},
		},
		{
			name: "valid request read only",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    testVolumeAttributes,
				Readonly:         true,
			},
			actions:       []mount.FakeAction{{Action: mount.FakeActionMount}},
			expectedMount: &mount.MountPoint{Device: testDevice, Path: testTargetPath, Type: "nfs", Opts: []string{"ro"}},
		},
		{
			name: "empty target path",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    testVolumeAttributes,
			},
			expectErr: true,
		},
		{
			name: "invalid volume capability",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeContext:    testVolumeAttributes,
			},
			expectErr: true,
		},
		{
			name: "invalid volume attribute",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeContext:    testVolumeAttributes,
			},
			expectErr: true,
		},
		// TODO add a test case for mount failure.
		// need to modify FakeMounter to be able to fail the mount call (and unmount)
	}

	for _, test := range cases {
		testEnv := initTestNodeServer(t)
		if test.mounts != nil {
			testEnv.fm.MountPoints = test.mounts
		}

		_, err = testEnv.ns.NodePublishVolume(context.TODO(), test.req)
		if !test.expectErr && err != nil {
			t.Errorf("test %q failed: %v", test.name, err)
		}
		if test.expectErr && err == nil {
			t.Errorf("test %q failed: got success", test.name)
		}

		validateMountPoint(t, test.name, testEnv.fm, test.expectedMount)
		//validateMountActions(t, testEnv.fm, test.name, test.actions)
	}
}

func TestNodeUnpublishVolume(t *testing.T) {
	defaultPerm := os.FileMode(0750) + os.ModeDir

	// Setup mount target path
	base, err := ioutil.TempDir("", "node-publish-")
	if err != nil {
		t.Fatalf("failed to setup testdir: %v", err)
	}
	testTargetPath := filepath.Join(base, "mount")
	if err = os.MkdirAll(testTargetPath, defaultPerm); err != nil {
		t.Fatalf("failed to setup target path: %v", err)
	}
	defer os.RemoveAll(base)

	cases := []struct {
		name          string
		mounts        []mount.MountPoint // already existing mounts
		req           *csi.NodeUnpublishVolumeRequest
		actions       []mount.FakeAction
		expectedMount *mount.MountPoint
		expectErr     bool
	}{
		{
			name:   "successful unmount",
			mounts: []mount.MountPoint{{Device: testDevice, Path: testTargetPath}},
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
			actions: []mount.FakeAction{{Action: mount.FakeActionUnmount}},
		},
		{
			name: "empty target path",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId: testVolumeID,
			},
			expectErr: true,
		},
		{
			name: "dir doesn't exist",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: "/node-unpublish-dir-not-exists",
			},
		},
		{
			name: "dir not mounted",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
		},
		// TODO:
		// mount check failed
		// unmount failed
	}

	for _, test := range cases {
		testEnv := initTestNodeServer(t)
		if test.mounts != nil {
			testEnv.fm.MountPoints = test.mounts
		}

		_, err = testEnv.ns.NodeUnpublishVolume(context.TODO(), test.req)
		if !test.expectErr && err != nil {
			t.Errorf("test %q failed: %v", test.name, err)
		}
		if test.expectErr && err == nil {
			t.Errorf("test %q failed: got success", test.name)
		}

		validateMountPoint(t, test.name, testEnv.fm, test.expectedMount)
		//validateMountActions(t, testEnv.fm, test.name, test.actions)
	}
}

func TestValidateVolumeAttributes(t *testing.T) {
	cases := []struct {
		name      string
		attrs     map[string]string
		expectErr bool
	}{
		{
			name: "valid attributes",
			attrs: map[string]string{
				attrIp:     "1.1.1.1",
				attrSourcePath: "vol1",
			},
		},
		{
			name: "invalid ip",
			attrs: map[string]string{
				attrSourcePath: "vol1",
			},
			expectErr: true,
		},
		{
			name: "invalid volume",
			attrs: map[string]string{
				attrIp: "1.1.1.1",
			},
			expectErr: true,
		},
	}

	for _, test := range cases {
		err := validateVolumeAttributes(test.attrs)
		if !test.expectErr && err != nil {
			t.Errorf("test %q failed: %v", test.name, err)
		}
		if test.expectErr && err == nil {
			t.Errorf("test %q failed: got success", test.name)
		}
	}
}

func TestNodeGetVolumeStats(t *testing.T) {
	defaultPerm := os.FileMode(0750) + os.ModeDir

	// Setup mount target path
	base, err := ioutil.TempDir("", "node-publish-")
	if err != nil {
		t.Fatalf("failed to setup testdir: %v", err)
	}
	testVolumePath := filepath.Join(base, "mount")
	if err = os.MkdirAll(testVolumePath, defaultPerm); err != nil {
		t.Fatalf("failed to setup volume path: %v", err)
	}
	stats, err := getVolumeStats(testVolumePath)
	if err != nil {
		t.Fatalf("failed to get stats of volume path: %v", err)
	}
	res := &csi.NodeGetVolumeStatsResponse{
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
	}
	defer os.RemoveAll(base)

	cases := []struct {
		name          string
		req           *csi.NodeGetVolumeStatsRequest
		expectErr     bool
	}{
		{
			name:   "successful",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId:   testVolumeID,
				VolumePath: testVolumePath,
			},
		},
		{
			name: "empty target path",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId: testVolumeID,
			},
			expectErr: true,
		},
		{
			name: "dir doesn't exist",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId:   testVolumeID,
				VolumePath: "/node-unpublish-dir-not-exists",
			},
			expectErr: true,
		},
	}

	for _, test := range cases {
		testEnv := initTestNodeServer(t)

		testres, err := testEnv.ns.NodeGetVolumeStats(context.TODO(), test.req)
		if !test.expectErr && err != nil {
			t.Errorf("test %q failed: %v", test.name, err)
		}
		if test.expectErr && err == nil {
			t.Errorf("test %q failed: got success", test.name)
		}
		if testres != nil {
			if !reflect.DeepEqual(res, testres) {
				t.Errorf("result not matched in case [%s]\nexpected : %v\nbut got  : %v", test.name, res, testres)
			}
		}
	}
}

func TestRegisterNodePrivateIP(t *testing.T) {
	// log
	flag.Set("logtostderr", "true")
	flag.Parse()

	// test k8s
	kubeobjects := []runtime.Object{}
	kubeobjects = append(kubeobjects, newCSINode("testNodeID", ""))
	kubeClient := k8sfake.NewSimpleClientset(kubeobjects...)
	config := &NifcloudNasDriverConfig{
		Name:          "testDriverName",
		Version:       "testDriverVersion",
		NodeID:        "testNodeID",
		RunController: false,
		RunNode:       true,
		KubeClient:    kubeClient,
		PrivateIfName: "lo",
		InitBackoff:   1,
	}

	driver, _ := NewNifcloudNasDriver(config)
	go func(){
		driver.Run("unix:/tmp/csi.sock")
	}()
	time.Sleep(time.Duration(2) * time.Second)
	driver.Stop()
	csinode, err := kubeClient.StorageV1().CSINodes().Get(context.TODO(), "testNodeID", metav1.GetOptions{})
	if err != nil {
		t.Errorf("Error obtaining result CSINode : %s", err.Error())
	}
	if csinode.GetAnnotations()[config.Name+"/privateIp"] != "127.0.0.1" {
		t.Errorf("Node IP not matched : %s", csinode.GetAnnotations()[config.Name+"/privateIp"])
	}
}

func TestNodeGetInfo(t *testing.T) {
	testEnv := initTestNodeServer(t)
	resp, err := testEnv.ns.NodeGetInfo(context.TODO(), nil)
	if err != nil {
		t.Fatalf("NodeGetInfo failed: %v", err)
	}
	if resp == nil {
		t.Fatalf("NodeGetInfo resp is nil")
	}
	if resp.NodeId != "testNodeID" {
		t.Errorf("got node id %v", resp.NodeId)
	}
	if resp.MaxVolumesPerNode != 128 {
		t.Errorf("got max volumes per node %v", resp.MaxVolumesPerNode)
	}
}

func TestNodeGetCapabilities(t *testing.T) {
	testEnv := initTestNodeServer(t)
	resp, err := testEnv.ns.NodeGetCapabilities(context.TODO(), nil)
	if err != nil {
		t.Fatalf("NodeGetCapabilities failed: %v", err)
	}
	if resp == nil {
		t.Fatalf("NodeGetCapabilities resp is nil")
	}
	if len(resp.Capabilities) != 1 ||
		!reflect.DeepEqual(resp.Capabilities[0], NewNodeServiceCapability(csi.NodeServiceCapability_RPC_GET_VOLUME_STATS)) {
		t.Errorf("got node capabilities %v", resp.Capabilities)
	}
}

func validateMountPoint(t *testing.T, name string, fm *mount.FakeMounter, e *mount.MountPoint) {
	if e == nil {
		if len(fm.MountPoints) != 0 {
			t.Errorf("test %q failed: got mounts %+v, expected none", name, fm.MountPoints)
		}
		return
	}

	if mLen := len(fm.MountPoints); mLen != 1 {
		t.Errorf("test %q failed: got %v mounts(%+v), expected %v", name, mLen, fm.MountPoints, 1)
		return
	}

	a := &fm.MountPoints[0]
	if a.Device != e.Device {
		t.Errorf("test %q failed: got device %q, expected %q", name, a.Device, e.Device)
	}
	if a.Path != e.Path {
		t.Errorf("test %q failed: got path %q, expected %q", name, a.Path, e.Path)
	}
	if a.Type != e.Type {
		t.Errorf("test %q failed: got type %q, expected %q", name, a.Type, e.Type)
	}

	// TODO: why does DeepEqual not work???
	aLen := len(a.Opts)
	eLen := len(e.Opts)
	if aLen != eLen {
		t.Errorf("test %q failed: got opts length %v, expected %v", name, aLen, eLen)
	} else {
		for i := range a.Opts {
			aOpt := a.Opts[i]
			eOpt := e.Opts[i]
			if aOpt != eOpt {
				t.Errorf("test %q failed: got opt %q, expected %q", name, aOpt, eOpt)
			}
		}
	}
}
