package driver

import (
	"fmt"
	"flag"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/context"
	"github.com/aws/aws-sdk-go-v2/aws/awserr"
	"github.com/nifcloud/nifcloud-sdk-go/service/nas"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

var (
	testZone = "testZone"
	testSecurityGroupName = "cluster-TESTCLUSTERUID"
	testCidrIP1 = "1.1.1.1/32"
	testCidrIP2 = "1.1.1.2/32"
	testCidrIP3 = "1.1.1.3/32"
	preObjects = []runtime.Object{
		newCSINode("testNodeID1", "1.1.1.1"),
		newCSINode("testNodeID2", "1.1.1.2"),
		newStorageClass("testStorageClass", testZone),
	}
	preSecurityGroup = nas.NASSecurityGroup{
		AvailabilityZone: &testZone,
		NASSecurityGroupName: &testSecurityGroupName,
		IPRanges: []nas.IPRange{
			nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
			nas.IPRange{CIDRIP: &testCidrIP2, Status: &statusAuthorized},
		},
	}
	lastSGInputs = map[string]nas.CreateNASSecurityGroupInput{
		testSecurityGroupName: nas.CreateNASSecurityGroupInput{
			AvailabilityZone: &testZone,
			NASSecurityGroupName: &testSecurityGroupName,
		},
	}
	lastIPs = map[string]bool{
		"1.1.1.1": true,
		"1.1.1.2": true,
	}
)

func TestSecuritygroupSync(t *testing.T) {

	cases := map[string]struct {
		pre_tsk bool
		pre_obj []runtime.Object
		pre_sgs []nas.NASSecurityGroup
		last_sgs *map[string]nas.CreateNASSecurityGroupInput
		last_ips *map[string]bool
		errmsg string
		exp_sgs []nas.NASSecurityGroup
		actions []string
		post_tsk bool
	}{
		"initialization1":{
			pre_tsk: true,
			pre_obj: preObjects,
			exp_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorizing},
				},
			}},
			actions: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
				"CreateNasSecurityGroup/" + testSecurityGroupName,
				"AuthorizeCIDRIP/" + testSecurityGroupName + "/1.1.1.1/32",
			},
			post_tsk: true,
		},
		"initialization2 wait authorizing ip1":{
			pre_tsk: true,
			pre_obj: preObjects,
			pre_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorizing},
				},
			}},
			last_sgs: &lastSGInputs,
			last_ips: &lastIPs,
			exp_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorizing},
				},
			}},
			actions: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
			},
			post_tsk: true,
		},
		"initialization3 authorize ip2":{
			pre_tsk: true,
			pre_obj: preObjects,
			pre_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
				},
			}},
			last_sgs: &lastSGInputs,
			last_ips: &lastIPs,
			exp_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
					nas.IPRange{CIDRIP: &testCidrIP2, Status: &statusAuthorizing},
				},
			}},
			actions: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
				"AuthorizeCIDRIP/" + testSecurityGroupName + "/1.1.1.2/32",
			},
			post_tsk: true,
		},
		"nothing changed (initializing4 completed)":{
			pre_tsk: true,
			pre_obj: preObjects,
			pre_sgs: []nas.NASSecurityGroup{preSecurityGroup},
			last_sgs: &lastSGInputs,
			last_ips: &lastIPs,
			exp_sgs: []nas.NASSecurityGroup{preSecurityGroup},
			actions: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
			},
			post_tsk: false,
		},
		"node added":{
			pre_tsk: true,
			pre_obj: []runtime.Object{
				newCSINode("testNodeID1", "1.1.1.1"),
				newCSINode("testNodeID2", "1.1.1.2"),
				newCSINode("testNodeID3", "1.1.1.3"),
				newStorageClass("testStorageClass", testZone),
			},
			pre_sgs: []nas.NASSecurityGroup{preSecurityGroup},
			last_sgs: &lastSGInputs,
			last_ips: &lastIPs,
			exp_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
					nas.IPRange{CIDRIP: &testCidrIP2, Status: &statusAuthorized},
					nas.IPRange{CIDRIP: &testCidrIP3, Status: &statusAuthorizing},
				},
			}},
			actions: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
				"AuthorizeCIDRIP/" + testSecurityGroupName + "/1.1.1.3/32",
			},
			post_tsk: true,
		},
		"node removed":{
			pre_tsk: true,
			pre_obj: []runtime.Object{
				newCSINode("testNodeID1", "1.1.1.1"),
				newStorageClass("testStorageClass", testZone),
			},
			pre_sgs: []nas.NASSecurityGroup{preSecurityGroup},
			last_sgs: &lastSGInputs,
			last_ips: &lastIPs,
			exp_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
					nas.IPRange{CIDRIP: &testCidrIP2, Status: &statusRevoking},
				},
			}},
			actions: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
				"RevokeCIDRIP/" + testSecurityGroupName + "/1.1.1.2/32",
			},
			post_tsk: true,
		},
		"wait other authorizing/revoking":{
			pre_tsk: true,
			pre_obj: []runtime.Object{
				newCSINode("testNodeID1", "1.1.1.1"),
				newStorageClass("testStorageClass", testZone),
			},
			pre_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
					nas.IPRange{CIDRIP: &testCidrIP2, Status: &statusAuthorized},
					nas.IPRange{CIDRIP: &testCidrIP3, Status: &statusRevoking},
				},
			}},
			last_sgs: &lastSGInputs,
			last_ips: &lastIPs,
			exp_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
					nas.IPRange{CIDRIP: &testCidrIP2, Status: &statusAuthorized},
					nas.IPRange{CIDRIP: &testCidrIP3, Status: &statusRevoking},
				},
			}},
			actions: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
			},
			post_tsk: true,
		},
	}

	flag.Set("logtostderr", "true")
	flag.Lookup("v").Value.Set("5")
	flag.Parse()

	for name, c := range(cases) {
		t.Logf("====== Test case [%s] :", name)

		syncer, _, cloud := initTestSecuritygroupSyncer(t, c.pre_obj, c.last_sgs, c.last_ips, c.pre_tsk)
		cloud.NasSecurityGroups = c.pre_sgs
		err := syncer.SyncNasSecurityGroups()
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
			} else {
				if !reflect.DeepEqual(c.exp_sgs, cloud.NasSecurityGroups) {
					t.Errorf("security group not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.exp_sgs, cloud.NasSecurityGroups)
				}
				if !reflect.DeepEqual(c.actions, cloud.Actions) {
					t.Errorf("cloud action not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.actions, cloud.Actions)
				}
				if c.post_tsk != syncer.DoesHaveTask() {
					t.Errorf("hasTask not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.post_tsk, syncer.DoesHaveTask())
				}
			}
		} else {
			if err == nil {
				t.Errorf("expected error not occured in case [%s]\nexpected : %s", name, c.errmsg)
			} else if !strings.Contains(err.Error(), c.errmsg) {
				t.Errorf("error message not matched in case [%s]\nmust contains : %s\nbut got : %s", name, c.errmsg, err.Error())
			}
		}
	}
}

func initTestSecuritygroupSyncer(
	t *testing.T,
	obj []runtime.Object,
	securityGroups *map[string]nas.CreateNASSecurityGroupInput,
	privateIps *map[string]bool, hasTask bool) (*NSGSyncer, kubernetes.Interface, *FakeCloud) {

	// test cloud
	cloud := newFakeCloud()
	// test k8s
	kubeobjects := []runtime.Object{}
	kubeobjects = append(kubeobjects, newNamespace("kube-system", "TESTCLUSTERUID"))
	kubeobjects = append(kubeobjects, obj...)
	kubeClient := k8sfake.NewSimpleClientset(kubeobjects...)

	driver := initTestDriver(t, cloud, kubeClient, true, false)
	return &NSGSyncer{
		driver: driver,
		SyncPeriod: 1, // seconds
		internalChkIntvl: 1, // seconds
		cloudChkIntvl: 1, // seconds
		hasTask: hasTask,
		lastNodePrivateIps: privateIps,
		lastSecurityGroupInputs: securityGroups,
	}, kubeClient, cloud
}

func newCSINode(name, ip string) *storagev1.CSINode {
	return &storagev1.CSINode{
		TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "CSINode"},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				"testDriverName/privateIp": ip,
			},
		},
		Spec: storagev1.CSINodeSpec{
			Drivers: []storagev1.CSINodeDriver{
				storagev1.CSINodeDriver{
					Name: "testDriverName",
					NodeID: "testNodeID",
				},
			},
		},
	}
}

func newStorageClass(name, zone string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Provisioner: "testDriverName",
		Parameters: map[string]string{
			"zone": zone,
		},
	}
}

// FakeCloud implementation

func (c *FakeCloud) GetNasSecurityGroup(ctx context.Context, name string) (*nas.NASSecurityGroup, error) {
	c.Actions = append(c.Actions, "GetNasSecurityGroup/" + name)
	for i, g := range(c.NasSecurityGroups) {
		if *g.NASSecurityGroupName == name {
			return &c.NasSecurityGroups[i], nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}

func (c *FakeCloud) CreateNasSecurityGroup(ctx context.Context, sc *nas.CreateNASSecurityGroupInput) (*nas.NASSecurityGroup, error) {
	c.Actions = append(c.Actions, "CreateNasSecurityGroup/" + *sc.NASSecurityGroupName)
	g := nas.NASSecurityGroup{
		AvailabilityZone: sc.AvailabilityZone,
		NASSecurityGroupName: sc.NASSecurityGroupName,
	}
	c.NasSecurityGroups = append(c.NasSecurityGroups, g)
	return &g, nil
}

func (c *FakeCloud) AuthorizeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error) {
	c.Actions = append(c.Actions, "AuthorizeCIDRIP/" + name + "/" + cidrip)
	for i, g := range(c.NasSecurityGroups) {
		if *g.NASSecurityGroupName == name {
			c.NasSecurityGroups[i].IPRanges = append(
				c.NasSecurityGroups[i].IPRanges,
				nas.IPRange{
					CIDRIP: &cidrip,
					Status: &statusAuthorizing,
				})
			return &c.NasSecurityGroups[i], nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}

func (c *FakeCloud) RevokeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error) {
	c.Actions = append(c.Actions, "RevokeCIDRIP/" + name + "/" + cidrip)
	for i, g := range(c.NasSecurityGroups) {
		if *g.NASSecurityGroupName == name {
			for j, r := range(c.NasSecurityGroups[i].IPRanges) {
				if *r.CIDRIP == cidrip {
					c.NasSecurityGroups[i].IPRanges[j].Status = &statusRevoking
				}
			}
			return &c.NasSecurityGroups[i], nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}
