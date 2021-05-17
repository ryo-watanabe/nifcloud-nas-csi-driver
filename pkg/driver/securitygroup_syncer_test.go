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
	lastSGInput = nas.CreateNASSecurityGroupInput{
		AvailabilityZone: &testZone,
		NASSecurityGroupName: &testSecurityGroupName,
	}
)

func initSecurityGroup() nas.NASSecurityGroup {
	return nas.NASSecurityGroup{
		AvailabilityZone: &testZone,
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
		pre_tsk bool
		pre_obj []runtime.Object
		pre_sgs []nas.NASSecurityGroup
		last_sg *nas.CreateNASSecurityGroupInput
		last_ips *map[string]bool
		errmsg string
		exp_sgs []nas.NASSecurityGroup
		alt_sgs []nas.NASSecurityGroup
		actions []string
		alt_act []string
		post_tsk bool
	}{
		"initialization1":{
			pre_tsk: true,
			pre_obj: initObjects(),
			exp_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorizing},
				},
			}},
			alt_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
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
			alt_act: []string{
				"GetNasSecurityGroup/" + testSecurityGroupName,
				"CreateNasSecurityGroup/" + testSecurityGroupName,
				"AuthorizeCIDRIP/" + testSecurityGroupName + "/1.1.1.2/32",
			},
			post_tsk: true,
		},
		"initialization2 wait authorizing ip1":{
			pre_tsk: true,
			pre_obj: initObjects(),
			pre_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorizing},
				},
			}},
			last_sg: &lastSGInput,
			last_ips: initLastIPs(),
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
			pre_obj: initObjects(),
			pre_sgs: []nas.NASSecurityGroup{nas.NASSecurityGroup{
				AvailabilityZone: &testZone,
				NASSecurityGroupName: &testSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{CIDRIP: &testCidrIP1, Status: &statusAuthorized},
				},
			}},
			last_sg: &lastSGInput,
			last_ips: initLastIPs(),
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
			pre_obj: initObjects(),
			pre_sgs: []nas.NASSecurityGroup{initSecurityGroup()},
			last_sg: &lastSGInput,
			last_ips: initLastIPs(),
			exp_sgs: []nas.NASSecurityGroup{initSecurityGroup()},
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
				newStorageClass("testStorageClass", testZone, "", ""),
			},
			pre_sgs: []nas.NASSecurityGroup{initSecurityGroup()},
			last_sg: &lastSGInput,
			last_ips: initLastIPs(),
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
				newStorageClass("testStorageClass", testZone, "", ""),
			},
			pre_sgs: []nas.NASSecurityGroup{initSecurityGroup()},
			last_sg: &lastSGInput,
			last_ips: initLastIPs(),
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
				newStorageClass("testStorageClass", testZone, "", ""),
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
			last_sg: &lastSGInput,
			last_ips: initLastIPs(),
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

		syncer, _, cloud := initTestSecuritygroupSyncer(t, c.pre_obj, c.last_sg, c.last_ips, c.pre_tsk)
		cloud.NasSecurityGroups = c.pre_sgs
		err := syncer.SyncNasSecurityGroups()
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
			} else {
				if !reflect.DeepEqual(c.exp_sgs, cloud.NasSecurityGroups) {
					if len(c.alt_sgs) > 0 && reflect.DeepEqual(c.alt_sgs, cloud.NasSecurityGroups) {
						t.Logf("security group matched alt_sgs")
					} else {
						t.Errorf("security group not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.exp_sgs, cloud.NasSecurityGroups)
					}
				}
				if !reflect.DeepEqual(c.actions, cloud.Actions) {
					if len(c.alt_act) > 0 && reflect.DeepEqual(c.alt_act, cloud.Actions) {
						t.Logf("cloud action matched alt_act")
					} else {
						t.Errorf("cloud action not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.actions, cloud.Actions)
					}
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
		driver: driver,
		SyncPeriod: 1, // seconds
		internalChkIntvl: 1, // seconds
		cloudChkIntvl: 1, // seconds
		hasTask: hasTask,
		lastNodePrivateIps: privateIps,
		lastSecurityGroupInput: securityGroup,
	}, kubeClient, cloud
}

func newCSINode(name, ip string) *storagev1.CSINode {
	csinode := &storagev1.CSINode{
		TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "CSINode"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{}},
		Spec: storagev1.CSINodeSpec{
			Drivers: []storagev1.CSINodeDriver{
				storagev1.CSINodeDriver{
					Name: "testDriverName",
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

func newStorageClass(name, zone, networkId, cidr string) *storagev1.StorageClass {
	class := &storagev1.StorageClass{
		TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Provisioner: "testDriverName",
		Parameters: map[string]string{},
	}
	if zone != "" {
		class.Parameters["zone"] = zone
	}
	if networkId != "" {
		class.Parameters["networkId"] = networkId
	}
	if cidr != "" {
		class.Parameters["reservedIpv4Cidr"] = cidr
	}
	return class
}

// FakeCloud implementation

func (c *FakeCloud) ChangeNasInstanceSecurityGroup(ctx context.Context, name, sgname string) (*nas.NASInstance, error) {
	c.Actions = append(c.Actions, "ChangeNasInstanceSecurityGroup/" + name + "/" + sgname)
	for i, n := range(c.NasInstances) {
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
