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
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
)

func initTestIdentityServer(t *testing.T) csi.IdentityServer {
	driver := initTestDriver(t, nil, nil, false, true)
	return driver.ids
}

func TestIdentityGetPluginInfo(t *testing.T) {
	s := initTestIdentityServer(t)

	resp, err := s.GetPluginInfo(context.TODO(), nil)
	if err != nil {
		t.Fatalf("GetPluginInfo failed: %v", err)
	}
	if resp == nil {
		t.Fatalf("GetPluginInfo resp is nil")
	}
	if resp.Name != "testDriverName" {
		t.Errorf("got driver name %v", resp.Name)
	}
	if resp.VendorVersion != "testDriverVersion" {
		t.Errorf("got driver version %v", resp.Name)
	}
}

func TestIdentityGetPluginCapabilities(t *testing.T) {
	s := initTestIdentityServer(t)
	resp, err := s.GetPluginCapabilities(context.TODO(), nil)
	if err != nil {
		t.Fatalf("GetPluginCapabilities failed: %v", err)
	}
	if resp == nil {
		t.Fatalf("GetPluginCapabilities resp is nil")
	}
	if len(resp.Capabilities) != 1 {
		t.Fatalf("returned %v capabilities", len(resp.Capabilities))
	}
	if resp.Capabilities[0].Type == nil {
		t.Fatalf("returned nil capability type")
	}
	service := resp.Capabilities[0].GetService()
	if service == nil {
		t.Fatalf("returned nil capability service")
	}
	if serviceType := service.GetType(); serviceType != csi.PluginCapability_Service_CONTROLLER_SERVICE {
		t.Fatalf("returned %v capability service", serviceType)
	}
}

func TestIdentityProbe(t *testing.T) {
	s := initTestIdentityServer(t)
	resp, err := s.Probe(context.TODO(), nil)
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}
	if resp == nil {
		t.Fatalf("Probe resp is nil")
	}
}
