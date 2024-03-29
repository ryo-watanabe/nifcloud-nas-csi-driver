package driver

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/awserr"
	"github.com/nifcloud/nifcloud-sdk-go/service/nas"
	"golang.org/x/net/context"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

var (
	testZone              = "testZone"
	testSecurityGroupName = "cluster-TESTCLUSTERUID"
	testCidrIP1           = "1.1.1.1/32"
	testCidrIP2           = "1.1.1.2/32"
	testCidrIP3           = "1.1.1.3/32"
	lastSGInput           = nas.CreateNASSecurityGroupInput{
		AvailabilityZone:     &testZone,
		NASSecurityGroupName: &testSecurityGroupName,
	}
)

func initSecurityGroup() nas.NASSecurityGroup {
	return nas.NASSecurityGroup{
		AvailabilityZone:     &testZone,
		NASSecurityGroupName: &testSecurityGroupName,
		IPRanges: []nas.IPRange{
			nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
			nas.IPRange{CIDRIP: &testCidrIP2, Status: &statusAuthorized},
		},
	}
}

func initObjects() []runtime.Object {
	return []runtime.Object{
		newCSINode("testNodeID1", "1.1.1.1"),
		newCSINode("testNodeID2", "1.1.1.2"),
		newStorageClass("testStorageClass", testZone, "", ""),
	}
}

func initLastIPs() *map[string]bool {
	return &map[string]bool{
		"1.1.1.1": true,
		"1.1.1.2": true,
	}
}

func TestSecuritygroupSync(t *testing.T) {

	cases := map[string]struct {
		preTsk  bool
		preObj  []runtime.Object
		preSgs  []nas.NASSecurityGroup
		lastSg  *nas.CreateNASSecurityGroupInput
		lastIps *map[string]bool
		errmsg  string
		expSgs  []nas.NASSecurityGroup
		altSgs  []nas.NASSecurityGroup
		actions []string
		altAct  []string
		postTsk bool
	}{
		"initialization1": {
			preTsk: true,
			preObj: initObjects(),
			expSgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone:     &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorizing},
				},
			}},
			altSgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone:     &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP2, Status: &statusAuthorizing},
				},
			}},
			actions: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
				"CreateNasSecurityGroup/" + testSecurityGroupName,
				"AuthorizeCIDRIP/" + testSecurityGroupName + "/1.1.1.1/32",
			},
			altAct: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
				"CreateNasSecurityGroup/" + testSecurityGroupName,
				"AuthorizeCIDRIP/" + testSecurityGroupName + "/1.1.1.2/32",
			},
			postTsk: true,
		},
		"initialization2 wait authorizing ip1": {
			preTsk: true,
			preObj: initObjects(),
			preSgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone:     &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorizing},
				},
			}},
			lastSg:  &lastSGInput,
			lastIps: initLastIPs(),
			expSgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone:     &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorizing},
				},
			}},
			actions: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
			},
			postTsk: true,
		},
		"initialization3 authorize ip2": {
			preTsk: true,
			preObj: initObjects(),
			preSgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone:     &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
				},
			}},
			lastSg:  &lastSGInput,
			lastIps: initLastIPs(),
			expSgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone:     &testZone,
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
			postTsk: true,
		},
		"nothing changed (initializing4 completed)": {
			preTsk:  true,
			preObj:  initObjects(),
			preSgs:  []nas.NASSecurityGroup{initSecurityGroup()},
			lastSg:  &lastSGInput,
			lastIps: initLastIPs(),
			expSgs:  []nas.NASSecurityGroup{initSecurityGroup()},
			actions: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
			},
			postTsk: false,
		},
		"node added": {
			preTsk: true,
			preObj: []runtime.Object{
				newCSINode("testNodeID1", "1.1.1.1"),
				newCSINode("testNodeID2", "1.1.1.2"),
				newCSINode("testNodeID3", "1.1.1.3"),
				newStorageClass("testStorageClass", testZone, "", ""),
			},
			preSgs:  []nas.NASSecurityGroup{initSecurityGroup()},
			lastSg:  &lastSGInput,
			lastIps: initLastIPs(),
			expSgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone:     &testZone,
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
			postTsk: true,
		},
		"node removed": {
			preTsk: true,
			preObj: []runtime.Object{
				newCSINode("testNodeID1", "1.1.1.1"),
				newStorageClass("testStorageClass", testZone, "", ""),
			},
			preSgs:  []nas.NASSecurityGroup{initSecurityGroup()},
			lastSg:  &lastSGInput,
			lastIps: initLastIPs(),
			expSgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone:     &testZone,
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
			postTsk: true,
		},
		"wait other authorizing/revoking": {
			preTsk: true,
			preObj: []runtime.Object{
				newCSINode("testNodeID1", "1.1.1.1"),
				newStorageClass("testStorageClass", testZone, "", ""),
			},
			preSgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone:     &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
					nas.IPRange{CIDRIP: &testCidrIP2, Status: &statusAuthorized},
					nas.IPRange{CIDRIP: &testCidrIP3, Status: &statusRevoking},
				},
			}},
			lastSg:  &lastSGInput,
			lastIps: initLastIPs(),
			expSgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone:     &testZone,
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
			postTsk: true,
		},
	}

	flagVSet("5")

	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)

		syncer, _, cloud := initTestSecuritygroupSyncer(t, c.preObj, c.lastSg, c.lastIps, c.preTsk)
		cloud.NasSecurityGroups = c.preSgs
		err := syncer.SyncNasSecurityGroups()
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
			} else {
				if !reflect.DeepEqual(c.expSgs, cloud.NasSecurityGroups) {
					if len(c.altSgs) > 0 && reflect.DeepEqual(c.altSgs, cloud.NasSecurityGroups) {
						t.Logf("security group matched altSgs")
					} else {
						t.Errorf(
							"security group not matched in case [%s]\nexpected : %v\nbut got  : %v",
							name, c.expSgs, cloud.NasSecurityGroups,
						)
					}
				}
				if !reflect.DeepEqual(c.actions, cloud.Actions) {
					if len(c.altAct) > 0 && reflect.DeepEqual(c.altAct, cloud.Actions) {
						t.Logf("cloud action matched altAct")
					} else {
						t.Errorf("cloud action not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.actions, cloud.Actions)
					}
				}
				if c.postTsk != syncer.DoesHaveTask() {
					t.Errorf("hasTask not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.postTsk, syncer.DoesHaveTask())
				}
			}
		} else {
			if err == nil {
				t.Errorf("expected error not occurred in case [%s]\nexpected : %s", name, c.errmsg)
			} else if !strings.Contains(err.Error(), c.errmsg) {
				t.Errorf("error message not matched in case [%s]\nmust contains : %s\nbut got : %s", name, c.errmsg, err.Error())
			}
		}
	}
}

func TestRunInitSecuritygroupSync(t *testing.T) {
	// log
	flagVSet("5")

	// test k8s
	kubeobjects := []runtime.Object{
		newPVC("testpvc", "100Gi", "TESTPVCUID"),
		newPV("pvc-TESTPVCUID", "testregion/pvc-TESTPVCUID", "100Gi"),
		newCSINode("testNodeID1", "192.168.0.1"),
		//newNamespace("kube-system", "TESTCLUSTERUID2"),
		newStorageClass("testStorageClass", testZone, "", ""),
	}
	kubeClient := k8sfake.NewSimpleClientset(kubeobjects...)

	// test cloud
	cloud := newFakeCloud()
	cloud.NasInstances = []nas.NASInstance{
		initNASInstance("pvc-TESTPVCUID", "192.168.100.0", 100),
	}

	config := &NifcloudNasDriverConfig{
		Name:          "testDriverName",
		Version:       "testDriverVersion",
		NodeID:        "testNodeID",
		RunController: true,
		RunNode:       false,
		KubeClient:    kubeClient,
		Cloud:         cloud,
		InitBackoff:   1,
		RestoreClstID: true,
		ClusterUID:    "TESTCLUSTERUID2",
	}

	driver, _ := NewNifcloudNasDriver(config)
	go func() {
		driver.Run("unix:/tmp/csi.sock")
	}()
	time.Sleep(time.Duration(2) * time.Second)
	driver.Stop()

	// check cloud actions
	expActions := []string{
		"GetNasSecurityGroup/cluster-TESTCLUSTERUID2",
		"CreateNasSecurityGroup/cluster-TESTCLUSTERUID2",
		"GetNasInstanceFromVolumeId/testregion/pvc-TESTPVCUID",
		"GetNasInstance/pvc-TESTPVCUID",
		"ChangeNasInstanceSecurityGroup/pvc-TESTPVCUID/cluster-TESTCLUSTERUID2",
		"AuthorizeCIDRIP/cluster-TESTCLUSTERUID2/192.168.0.1/32",
	}
	if !reflect.DeepEqual(expActions, cloud.Actions) {
		t.Errorf("cloud action not matched\nexpected : %v\nbut got  : %v", expActions, cloud.Actions)
	}

	// check nas security group name
	exp := initNASInstance("pvc-TESTPVCUID", "192.168.100.0", 100)
	sgname, _ := getSecurityGroupName(context.TODO(), driver)
	exp.NASSecurityGroups[0].NASSecurityGroupName = &sgname
	exp.NASInstanceStatus = &statusModifying
	got, err := cloud.GetNasInstance(context.TODO(), "pvc-TESTPVCUID")
	if err != nil {
		t.Errorf("error getting nas instance : %s", err.Error())
	}
	if !reflect.DeepEqual(exp, *got) {
		t.Errorf("NASInstance not matched\nexpected : %v\nbut got  : %v", exp, got)
	}
}

func initTestSecuritygroupSyncer(
	t *testing.T,
	obj []runtime.Object,
	securityGroup *nas.CreateNASSecurityGroupInput,
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
		driver:                 driver,
		SyncPeriod:             1, // seconds
		internalChkIntvl:       1, // seconds
		cloudChkIntvl:          1, // seconds
		hasTask:                hasTask,
		lastNodePrivateIps:     privateIps,
		lastSecurityGroupInput: securityGroup,
	}, kubeClient, cloud
}

func newCSINode(name, ip string) *storagev1.CSINode {
	csinode := &storagev1.CSINode{
		TypeMeta:   metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "CSINode"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{}},
		Spec: storagev1.CSINodeSpec{
			Drivers: []storagev1.CSINodeDriver{
				storagev1.CSINodeDriver{
					Name:   "testDriverName",
					NodeID: "testNodeID",
				},
			},
		},
	}
	if ip != "" {
		csinode.ObjectMeta.Annotations["testDriverName/privateIp"] = ip
	}
	return csinode
}

func newStorageClass(name, zone, networkID, cidr string) *storagev1.StorageClass {
	class := &storagev1.StorageClass{
		TypeMeta:    metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"},
		ObjectMeta:  metav1.ObjectMeta{Name: name},
		Provisioner: "testDriverName",
		Parameters:  map[string]string{},
	}
	if zone != "" {
		class.Parameters["zone"] = zone
	}
	if networkID != "" {
		class.Parameters["networkId"] = networkID
	}
	if cidr != "" {
		class.Parameters["reservedIpv4Cidr"] = cidr
	}
	return class
}

// FakeCloud implementation

func (c *FakeCloud) ChangeNasInstanceSecurityGroup(ctx context.Context, name, sgname string) (*nas.NASInstance, error) {
	c.Actions = append(c.Actions, "ChangeNasInstanceSecurityGroup/"+name+"/"+sgname)
	for i, n := range c.NasInstances {
		if *n.NASInstanceIdentifier == name {
			c.NasInstances[i].NASInstanceStatus = &statusModifying
			c.NasInstances[i].NASSecurityGroups = []nas.NASSecurityGroup{
				nas.NASSecurityGroup{NASSecurityGroupName: &sgname},
			}
			c.waitCnt = 2
			return &c.NasInstances[i], nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}

func (c *FakeCloud) GetNasSecurityGroup(ctx context.Context, name string) (*nas.NASSecurityGroup, error) {
	c.Actions = append(c.Actions, "GetNasSecurityGroup/"+name)
	for i, g := range c.NasSecurityGroups {
		if *g.NASSecurityGroupName == name {
			return &c.NasSecurityGroups[i], nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}

func (c *FakeCloud) CreateNasSecurityGroup(
	ctx context.Context, sc *nas.CreateNASSecurityGroupInput) (*nas.NASSecurityGroup, error) {
	c.Actions = append(c.Actions, "CreateNasSecurityGroup/"+*sc.NASSecurityGroupName)
	g := nas.NASSecurityGroup{
		AvailabilityZone:     sc.AvailabilityZone,
		NASSecurityGroupName: sc.NASSecurityGroupName,
	}
	c.NasSecurityGroups = append(c.NasSecurityGroups, g)
	return &g, nil
}

func (c *FakeCloud) AuthorizeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error) {
	c.Actions = append(c.Actions, "AuthorizeCIDRIP/"+name+"/"+cidrip)
	for i, g := range c.NasSecurityGroups {
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
	c.Actions = append(c.Actions, "RevokeCIDRIP/"+name+"/"+cidrip)
	for i, g := range c.NasSecurityGroups {
		if *g.NASSecurityGroupName == name {
			for j, r := range c.NasSecurityGroups[i].IPRanges {
				if *r.CIDRIP == cidrip {
					c.NasSecurityGroups[i].IPRanges[j].Status = &statusRevoking
				}
			}
			return &c.NasSecurityGroups[i], nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}
