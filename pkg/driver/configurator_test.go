package driver

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/aws/aws-sdk-go-v2/aws/awserr"
	"github.com/nifcloud/nifcloud-sdk-go/service/computing"
	"github.com/nifcloud/nifcloud-sdk-go/service/hatoba"
	"github.com/nifcloud/nifcloud-sdk-go/service/rdb"
	"golang.org/x/net/context"
)

func TestConfigurator(t *testing.T) {

	cases := map[string]struct {
		instances []computing.InstancesSet
		nodes     []hatoba.Node
		networkID string
		preObj    []runtime.Object
		lanCidr   string
		routerID  string
		dbIP      string
		poolStart string
		poolStop  string
		dhcpIps   []string
		errmsg    string
		actions   []string
		postConf  *Configurator
		update    bool
	}{
		"init private LAN": {
			instances: []computing.InstancesSet{
				initInstance("111.0.0.1", "192.168.0.1", "net-TestPrivate", "testZone"),
				initInstance("111.0.0.2", "192.168.0.2", "net-TestPrivate", "testZone"),
			},
			networkID: "net-TestPrivate",
			lanCidr:   "192.168.0.0/16",
			routerID:  "testRouterId",
			dbIP:      "192.168.200.1",
			poolStart: "192.168.10.1",
			poolStop:  "192.168.10.254",
			dhcpIps: []string{
				"192.168.100.1",
				"192.168.100.2",
				"192.168.100.3",
			},
			preObj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", ""),
				newCSINode("testNodeID2", ""),
				newStorageClass("testStorageClass", "testZone", "", ""),
			},
			actions: []string{
				"ListClusters",
				"ListInstances",
				"GetPrivateLan/net-TestPrivate",
				"ListRdbInstances",
				"GetDhcpStatus/net-TestPrivate/testRouterId",
			},
			postConf: &Configurator{
				networkID: "net-TestPrivate",
				zone:      "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
				},
				cidr: "192.168.128.0/20",
			},
		},
		"init common private": {
			instances: []computing.InstancesSet{
				initInstance("111.0.0.1", "10.100.0.1", "net-COMMON_PRIVATE", "testZone"),
				initInstance("111.0.0.2", "10.100.0.2", "net-COMMON_PRIVATE", "testZone"),
			},
			preObj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", ""),
				newCSINode("testNodeID2", ""),
				newStorageClass("testStorageClass", "testZone", "", ""),
			},
			actions: []string{
				"ListClusters",
				"ListInstances",
			},
			postConf: &Configurator{
				networkID: "net-COMMON_PRIVATE",
				zone:      "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "10.100.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "10.100.0.2"},
				},
				cidr: "",
			},
		},
		"init hatoba private LAN": {
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
			},
			networkID: "net-TestPrivate",
			lanCidr:   "192.168.0.0/24",
			routerID:  "testRouterId",
			poolStart: "192.168.0.64",
			poolStop:  "192.168.0.127",
			preObj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", ""),
				newCSINode("testNodeID2", ""),
				newStorageClass("testStorageClass", "testZone", "", ""),
			},
			actions: []string{
				"ListClusters",
				"GetPrivateLan/net-TestPrivate",
				"ListRdbInstances",
				"GetDhcpStatus/net-TestPrivate/testRouterId",
			},
			postConf: &Configurator{
				networkID: "net-TestPrivate",
				zone:      "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
				},
				cidr: "192.168.0.160/27",
			},
		},
		"not found": {
			errmsg: "nodes not found in computing / hatoba",
		},
		"private LAN not found": {
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
			},
			networkID: "net-NotFound",
			preObj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", ""),
				newCSINode("testNodeID2", ""),
				newStorageClass("testStorageClass", "testZone", "", ""),
			},
			errmsg: "getting private lan net-NotFound : TestAwsErrorNotFound",
		},
		"update nothing changed": {
			update: true,
			preObj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", "192.168.0.1"),
				newCSINode("testNodeID2", "192.168.0.2"),
				newStorageClass("testStorageClass", "testZone", "net-TestPrivate", "192.168.0.160/27"),
			},
			postConf: &Configurator{
				networkID: "net-TestPrivate",
				zone:      "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
				},
			},
		},
		"update node added": {
			update: true,
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
				initNode("111.0.0.3", "192.168.0.3", "testZone"),
			},
			networkID: "net-TestPrivate",
			preObj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newNode("testNodeID3", "111.0.0.3", ""),
				newCSINode("testNodeID1", "192.168.0.1"),
				newCSINode("testNodeID2", "192.168.0.2"),
				newCSINode("testNodeID3", ""),
				newStorageClass("testStorageClass", "testZone", "net-TestPrivate", "192.168.0.160/27"),
			},
			actions: []string{
				"ListClusters",
			},
			postConf: &Configurator{
				networkID: "net-TestPrivate",
				zone:      "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
					NodeConfig{name: "testNodeID3", publicIP: "111.0.0.3", privateIP: "192.168.0.3"},
				},
			},
		},
		"update zone/networkId different between classes": {
			update: true,
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
			},
			networkID: "net-COMMON_PRIVATE",
			preObj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", "192.168.0.1"),
				newCSINode("testNodeID2", "192.168.0.2"),
				newStorageClass("testStorageClass", "testZone", "net-COMMON_PRIVATE", ""),
				newStorageClass("testStorageClass2", "testZone2", "net-TestPrivate", "192.168.0.160/27"),
			},
			actions: []string{
				"ListClusters",
			},
			postConf: &Configurator{
				networkID: "net-COMMON_PRIVATE",
				zone:      "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
				},
			},
		},
		"init cidr block too small": {
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
			},
			networkID: "net-TestPrivate",
			lanCidr:   "192.168.0.0/28",
			preObj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", ""),
				newCSINode("testNodeID2", ""),
				newStorageClass("testStorageClass", "testZone", "", ""),
			},
			errmsg: "initialing recommended cidr divs : mask offset must be positive value",
		},
		"init cannot determine cidr": {
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
			},
			networkID: "net-TestPrivate",
			lanCidr:   "192.168.0.0/24",
			routerID:  "testRouterId",
			poolStart: "192.168.0.32",
			poolStop:  "192.168.0.127",
			dbIP:      "192.168.0.130",
			dhcpIps: []string{
				"192.168.0.161",
				"192.168.0.193",
			},
			preObj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", ""),
				newCSINode("testNodeID2", ""),
				newStorageClass("testStorageClass", "testZone", "", ""),
			},
			actions: []string{
				"ListClusters",
				"GetPrivateLan/net-TestPrivate",
				"ListRdbInstances",
				"GetDhcpStatus/net-TestPrivate/testRouterId",
			},
			postConf: &Configurator{
				networkID: "net-TestPrivate",
				zone:      "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
				},
			},
		},
		"nodes without public IPs": {
			update: true,
			preObj: []runtime.Object{
				newNode("testNodeID1", "10.100.0.1", "172.20.0.1"),
				newNode("testNodeID2", "10.100.0.2", "172.20.0.2"),
				newCSINode("testNodeID1", "172.20.0.1"),
				newCSINode("testNodeID2", "172.20.0.2"),
				newStorageClass("testStorageClass", "testZone", "net-TestPrivate", "192.168.0.160/27"),
			},
			errmsg: "node testNodeID1 does not have public IP",
		},
	}

	flagVSet("5")

	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)

		conf, _, cloud := initTestConfigurator(t, c.preObj)
		cloud.Instances = c.instances
		initCluster("testCluster", c.networkID, c.nodes)
		cidrBlock = c.lanCidr
		routerID = c.routerID
		poolStart = c.poolStart
		poolStop = c.poolStop
		setDhcpIPAddresses(c.dhcpIps)
		initDB(c.dbIP, c.networkID)

		var err error
		if c.update {
			err = conf.Update()
		} else {
			err = conf.Init()
		}

		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
			} else {
				if !reflect.DeepEqual(c.actions, cloud.Actions) {
					t.Errorf("cloud action not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.actions, cloud.Actions)
				}
				chkConfig(t, c.postConf, conf, name)
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

func TestRunConfigurator(t *testing.T) {
	// log
	flagVSet("5")

	// test k8s
	kubeobjects := []runtime.Object{
		newNode("testNodeID1", "111.0.0.1", ""),
		newCSINode("testNodeID1", ""),
		newNamespace("kube-system", "TESTCLUSTERUID"),
	}
	kubeClient := k8sfake.NewSimpleClientset(kubeobjects...)

	// test cloud
	cloud := newFakeCloud()
	initCluster(
		"testCluster",
		"net-COMMON_PRIVATE",
		[]hatoba.Node{
			initNode("111.0.0.1", "192.168.0.1", "testZone"),
		},
	)

	config := &NifcloudNasDriverConfig{
		Name:          "testDriverName",
		Version:       "testDriverVersion",
		NodeID:        "testNodeID",
		RunController: true,
		RunNode:       false,
		KubeClient:    kubeClient,
		Cloud:         cloud,
		InitBackoff:   1,
		Configurator:  true,
	}

	driver, _ := NewNifcloudNasDriver(config)
	go func() {
		driver.Run("unix:/tmp/csi.sock")
	}()
	time.Sleep(time.Duration(2) * time.Second)
	driver.Stop()

	// check cloud actions
	expActions := []string{
		"ListClusters",
		"GetNasSecurityGroup/cluster-TESTCLUSTERUID",
		"CreateNasSecurityGroup/cluster-TESTCLUSTERUID",
		"AuthorizeCIDRIP/cluster-TESTCLUSTERUID/192.168.0.1/32",
	}
	if !reflect.DeepEqual(expActions, cloud.Actions) {
		t.Errorf("cloud action not matched\nexpected : %v\nbut got  : %v", expActions, cloud.Actions)
	}

	// check storage class parameters
	exp := newStorageClass("csi-nifcloud-nas-std", "testZone", "net-COMMON_PRIVATE", "")
	exp.Parameters["instanceType"] = "0"
	got, err := kubeClient.StorageV1().StorageClasses().Get(context.TODO(), "csi-nifcloud-nas-std", metav1.GetOptions{})
	if err != nil {
		t.Errorf("error getting storageclass : %s", err.Error())
	}
	if !reflect.DeepEqual(exp, got) {
		t.Errorf("storageclass not matched\nexpected : %v\nbut got  : %v", exp, got)
	}
}

func initTestConfigurator(t *testing.T, obj []runtime.Object) (*Configurator, kubernetes.Interface, *FakeCloud) {
	// test cloud
	cloud := newFakeCloud()
	// test k8s
	kubeobjects := []runtime.Object{}
	kubeobjects = append(kubeobjects, newNamespace("kube-system", "TESTCLUSTERUID"))
	kubeobjects = append(kubeobjects, obj...)
	kubeClient := k8sfake.NewSimpleClientset(kubeobjects...)

	driver := initTestDriver(t, cloud, kubeClient, true, false)
	driver.config.Configurator = true
	return newConfigurator(driver), kubeClient, cloud
}

func chkConfig(t *testing.T, exp, got *Configurator, name string) {
	if exp.networkID != got.networkID {
		t.Errorf("networkId not matched in case [%s] exp:%s but got:%s", name, exp.networkID, got.networkID)
	}
	if exp.zone != got.zone {
		t.Errorf("zone not matched in case [%s] exp:%s but got:%s", name, exp.zone, got.zone)
	}
	if exp.cidr != got.cidr {
		t.Errorf("cidr not matched in case [%s] exp:%s but got:%s", name, exp.cidr, got.cidr)
	}
	if !reflect.DeepEqual(exp.nodes, got.nodes) {
		t.Errorf("nodes not matched in case [%s]\nexpected : %v\nbut got  : %v", name, exp.nodes, got.nodes)
	}
}

func newNode(name, external, internal string) *corev1.Node {
	node := &corev1.Node{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				corev1.NodeAddress{
					Type:    corev1.NodeExternalIP,
					Address: external,
				},
			},
		},
	}
	if external != "" {
		node.Status.Addresses = append(node.Status.Addresses, corev1.NodeAddress{
			Type:    corev1.NodeExternalIP,
			Address: external,
		})
	}
	if internal != "" {
		node.Status.Addresses = append(node.Status.Addresses, corev1.NodeAddress{
			Type:    corev1.NodeInternalIP,
			Address: internal,
		})
	}
	return node
}

func initInstance(ip, privateIP, networkID, zone string) computing.InstancesSet {
	return computing.InstancesSet{
		IpAddress:        &ip,
		PrivateIpAddress: &privateIP,
		NetworkInterfaceSet: []computing.NetworkInterfaceSetOfDescribeInstances{
			computing.NetworkInterfaceSetOfDescribeInstances{
				NiftyNetworkId:   &networkID,
				PrivateIpAddress: &privateIP,
			},
		},
		Placement: &computing.Placement{
			AvailabilityZone: &zone,
		},
	}
}

func initNode(publicIP, privateIP, zone string) hatoba.Node {
	return hatoba.Node{
		PublicIpAddress:  &publicIP,
		PrivateIpAddress: &privateIP,
		AvailabilityZone: &zone,
	}
}

// FakeCloud implementations

var clusters []hatoba.Cluster

func initCluster(name, networkID string, nodes []hatoba.Node) {
	clusters = []hatoba.Cluster{}
	if len(nodes) > 0 {
		clusters = append(clusters, hatoba.Cluster{
			Name:          &name,
			NetworkConfig: &hatoba.NetworkConfig{NetworkId: &networkID},
			NodePools: []hatoba.NodePool{
				hatoba.NodePool{Nodes: nodes},
			},
		})
	}
}

// hatoba
func (c *FakeCloud) ListClusters(ctx context.Context) ([]hatoba.Cluster, error) {
	c.Actions = append(c.Actions, "ListClusters")
	return clusters, nil
}

// computing
func (c *FakeCloud) ListInstances(ctx context.Context) ([]computing.InstancesSet, error) {
	c.Actions = append(c.Actions, "ListInstances")
	return c.Instances, nil
}

var cidrBlock, routerID string

func (c *FakeCloud) GetPrivateLan(ctx context.Context, networkID string) (*computing.PrivateLanSet, error) {
	c.Actions = append(c.Actions, "GetPrivateLan/"+networkID)
	if networkID == "net-TestPrivate" {
		return &computing.PrivateLanSet{
			CidrBlock: &cidrBlock,
			RouterSet: []computing.RouterSetOfNiftyDescribePrivateLans{
				computing.RouterSetOfNiftyDescribePrivateLans{
					RouterId: &routerID,
				},
			},
		}, nil
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("Private Lan (networkId=%s) not found", networkID))
}

var poolStart, poolStop string
var dhcpIPAddresses []computing.DhcpIpAddressSet

func setDhcpIPAddresses(ips []string) {
	dhcpIPAddresses = []computing.DhcpIpAddressSet{}
	for i := 0; i < len(ips); i++ {
		dhcpIPAddresses = append(dhcpIPAddresses, computing.DhcpIpAddressSet{
			IpAddress: &ips[i],
		})
	}
}

func (c *FakeCloud) GetDhcpStatus(ctx context.Context, networkID, routerID string) (
	[]computing.IpAddressPoolSet, []computing.DhcpIpAddressSet, error) {
	c.Actions = append(c.Actions, "GetDhcpStatus/"+networkID+"/"+routerID)
	pools := []computing.IpAddressPoolSet{
		computing.IpAddressPoolSet{
			StartIpAddress: &poolStart,
			StopIpAddress:  &poolStop,
		},
	}
	return pools, dhcpIPAddresses, nil
}

// rdb

var dbs []rdb.DBInstance

func initDB(ip, networkID string) {
	dbs = []rdb.DBInstance{}
	if ip != "" {
		dbs = append(dbs, rdb.DBInstance{
			NiftyNetworkId:            &networkID,
			NiftyMasterPrivateAddress: &ip,
		})
	}
}

func (c *FakeCloud) ListRdbInstances(ctx context.Context) ([]rdb.DBInstance, error) {
	c.Actions = append(c.Actions, "ListRdbInstances")
	return dbs, nil
}
