package driver

import (
	"fmt"
	"flag"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"golang.org/x/net/context"
	"github.com/aws/aws-sdk-go-v2/aws/awserr"
	"github.com/nifcloud/nifcloud-sdk-go/service/computing"
	"github.com/nifcloud/nifcloud-sdk-go/service/hatoba"
	"github.com/nifcloud/nifcloud-sdk-go/service/rdb"
)

func TestConfigurator(t *testing.T) {

	cases := map[string]struct {
		instances []computing.InstancesSet
		nodes []hatoba.Node
		network_id string
		pre_obj []runtime.Object
		lan_cidr string
		router_id string
		db_ip string
		pool_start string
		pool_stop string
		dhcp_ips []string
		errmsg string
		actions []string
		post_conf *Configurator
		update bool
	}{
		"init private LAN":{
			instances: []computing.InstancesSet{
				initInstance("111.0.0.1", "192.168.0.1", "net-TestPrivate", "testZone"),
				initInstance("111.0.0.2", "192.168.0.2", "net-TestPrivate", "testZone"),
			},
			network_id: "net-TestPrivate",
			lan_cidr: "192.168.0.0/16",
			router_id: "testRouterId",
			db_ip: "192.168.200.1",
			pool_start: "192.168.10.1",
			pool_stop: "192.168.10.254",
			dhcp_ips: []string{
				"192.168.100.1",
				"192.168.100.2",
				"192.168.100.3",
			},
			pre_obj: []runtime.Object{
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
			post_conf: &Configurator{
				networkId: "net-TestPrivate",
				zone: "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
				},
				cidr: "192.168.128.0/20",
			},
		},
		"init common private":{
			instances: []computing.InstancesSet{
				initInstance("111.0.0.1", "10.100.0.1", "net-COMMON_PRIVATE", "testZone"),
				initInstance("111.0.0.2", "10.100.0.2", "net-COMMON_PRIVATE", "testZone"),
			},
			pre_obj: []runtime.Object{
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
			post_conf: &Configurator{
				networkId: "net-COMMON_PRIVATE",
				zone: "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "10.100.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "10.100.0.2"},
				},
				cidr: "",
			},
		},
		"init hatoba private LAN":{
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
			},
			network_id: "net-TestPrivate",
			lan_cidr: "192.168.0.0/24",
			router_id: "testRouterId",
			pool_start: "192.168.0.64",
			pool_stop: "192.168.0.127",
			pre_obj: []runtime.Object{
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
			post_conf: &Configurator{
				networkId: "net-TestPrivate",
				zone: "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
				},
				cidr: "192.168.0.160/27",
			},
		},
		"not found" : {
			errmsg: "nodes not found in computing / hatoba",
		},
		"private LAN not found":{
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
			},
			network_id: "net-NotFound",
			pre_obj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", ""),
				newCSINode("testNodeID2", ""),
				newStorageClass("testStorageClass", "testZone", "", ""),
			},
			errmsg: "getting private lan net-NotFound : TestAwsErrorNotFound",
		},
		"update nothing changed":{
			update: true,
			pre_obj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", "192.168.0.1"),
				newCSINode("testNodeID2", "192.168.0.2"),
				newStorageClass("testStorageClass", "testZone", "net-TestPrivate", "192.168.0.160/27"),
			},
			post_conf: &Configurator{
				networkId: "net-TestPrivate",
				zone: "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
				},
			},
		},
		"update node added":{
			update: true,
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
				initNode("111.0.0.3", "192.168.0.3", "testZone"),
			},
			network_id: "net-TestPrivate",
			pre_obj: []runtime.Object{
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
			post_conf: &Configurator{
				networkId: "net-TestPrivate",
				zone: "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
					NodeConfig{name: "testNodeID3", publicIP: "111.0.0.3", privateIP: "192.168.0.3"},
				},
			},
		},
		"update zone/networkId different between classes":{
			update: true,
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
			},
			network_id: "net-COMMON_PRIVATE",
			pre_obj: []runtime.Object{
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
			post_conf: &Configurator{
				networkId: "net-COMMON_PRIVATE",
				zone: "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
				},
			},
		},
		"init cidr block too small":{
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
			},
			network_id: "net-TestPrivate",
			lan_cidr: "192.168.0.0/28",
			pre_obj: []runtime.Object{
				newNode("testNodeID1", "111.0.0.1", ""),
				newNode("testNodeID2", "111.0.0.2", ""),
				newCSINode("testNodeID1", ""),
				newCSINode("testNodeID2", ""),
				newStorageClass("testStorageClass", "testZone", "", ""),
			},
			errmsg: "initialing recommended cidr divs : mask offset must be positive value",
		},
		"init cannot determine cidr":{
			nodes: []hatoba.Node{
				initNode("111.0.0.1", "192.168.0.1", "testZone"),
				initNode("111.0.0.2", "192.168.0.2", "testZone"),
			},
			network_id: "net-TestPrivate",
			lan_cidr: "192.168.0.0/24",
			router_id: "testRouterId",
			pool_start: "192.168.0.32",
			pool_stop: "192.168.0.127",
			db_ip: "192.168.0.130",
			dhcp_ips: []string{
				"192.168.0.161",
				"192.168.0.193",
			},
			pre_obj: []runtime.Object{
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
			post_conf: &Configurator{
				networkId: "net-TestPrivate",
				zone: "testZone",
				nodes: []NodeConfig{
					NodeConfig{name: "testNodeID1", publicIP: "111.0.0.1", privateIP: "192.168.0.1"},
					NodeConfig{name: "testNodeID2", publicIP: "111.0.0.2", privateIP: "192.168.0.2"},
				},
			},
		},
		"nodes without public IPs":{
			update: true,
			pre_obj: []runtime.Object{
				newNode("testNodeID1", "10.100.0.1", "172.20.0.1"),
				newNode("testNodeID2", "10.100.0.2", "172.20.0.2"),
				newCSINode("testNodeID1", "172.20.0.1"),
				newCSINode("testNodeID2", "172.20.0.2"),
				newStorageClass("testStorageClass", "testZone", "net-TestPrivate", "192.168.0.160/27"),
			},
			errmsg: "node testNodeID1 does not have public IP",
		},
	}

	flag.Set("logtostderr", "true")
	flag.Lookup("v").Value.Set("5")
	flag.Parse()

	for name, c := range(cases) {
		t.Logf("====== Test case [%s] :", name)

		conf, _, cloud := initTestConfigurator(t, c.pre_obj)
		cloud.Instances = c.instances
		initCluster("testCluster", c.network_id, c.nodes)
		cidrBlock = c.lan_cidr
		routerId = c.router_id
		poolStart = c.pool_start
		poolStop = c.pool_stop
		setDhcpIpAddresses(c.dhcp_ips)
		initDB(c.db_ip, c.network_id)

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
				chkConfig(t, c.post_conf, conf, name)
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

func TestRunConfigurator(t *testing.T) {
	// log
	flag.Set("logtostderr", "true")
	flag.Lookup("v").Value.Set("5")
	flag.Parse()

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
	go func(){
		driver.Run("unix:/tmp/csi.sock")
	}()
	time.Sleep(time.Duration(2) * time.Second)
	driver.Stop()

	// check cloud actions
	exp_actions := []string{
		"ListClusters",
		"GetNasSecurityGroup/cluster-TESTCLUSTERUID",
		"CreateNasSecurityGroup/cluster-TESTCLUSTERUID",
		"AuthorizeCIDRIP/cluster-TESTCLUSTERUID/192.168.0.1/32",
	}
	if !reflect.DeepEqual(exp_actions, cloud.Actions) {
		t.Errorf("cloud action not matched\nexpected : %v\nbut got  : %v", exp_actions, cloud.Actions)
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
	if exp.networkId != got.networkId {
		t.Errorf("networkId not matched in case [%s] exp:%s but got:%s", name, exp.networkId, got.networkId)
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
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				corev1.NodeAddress{
					Type: corev1.NodeExternalIP,
					Address: external,
				},
			},
		},
	}
	if external != "" {
		node.Status.Addresses = append(node.Status.Addresses, corev1.NodeAddress{
			Type: corev1.NodeExternalIP,
			Address: external,
		})
	}
	if internal != "" {
		node.Status.Addresses = append(node.Status.Addresses, corev1.NodeAddress{
			Type: corev1.NodeInternalIP,
			Address: internal,
		})
	}
	return node
}

func initInstance(ip, privateIp, networkId, zone string) computing.InstancesSet {
	return computing.InstancesSet{
		IpAddress: &ip,
		PrivateIpAddress: &privateIp,
		NetworkInterfaceSet: []computing.NetworkInterfaceSetOfDescribeInstances{
			computing.NetworkInterfaceSetOfDescribeInstances{
				NiftyNetworkId: &networkId,
				PrivateIpAddress: &privateIp,
			},
		},
		Placement: &computing.Placement{
			AvailabilityZone: &zone,
		},
	}
}

func initNode(publicIp, privateIp, zone string) hatoba.Node {
	return hatoba.Node{
		PublicIpAddress: &publicIp,
		PrivateIpAddress: &privateIp,
		AvailabilityZone: &zone,
	}
}

// FakeCloud implementations

var clusters []hatoba.Cluster

func initCluster(name, networkId string, nodes []hatoba.Node) {
	clusters = []hatoba.Cluster{}
	if len(nodes) > 0 {
		clusters = append(clusters, hatoba.Cluster{
			Name: &name,
			NetworkConfig: &hatoba.NetworkConfig{NetworkId: &networkId},
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

var cidrBlock, routerId string

func (c *FakeCloud) GetPrivateLan(ctx context.Context, networkId string) (*computing.PrivateLanSet, error) {
	c.Actions = append(c.Actions, "GetPrivateLan/" + networkId)
	if networkId == "net-TestPrivate" {
		return &computing.PrivateLanSet{
			CidrBlock: &cidrBlock,
			RouterSet: []computing.RouterSetOfNiftyDescribePrivateLans{
				computing.RouterSetOfNiftyDescribePrivateLans{
					RouterId: &routerId,
				},
			},
		}, nil
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("Private Lan (networkId=%s) not found", networkId))
}

var poolStart, poolStop string
var dhcpIpAddresses []computing.DhcpIpAddressSet

func setDhcpIpAddresses(ips []string) {
	dhcpIpAddresses = []computing.DhcpIpAddressSet{}
	for i := 0; i < len(ips); i++ {
		dhcpIpAddresses = append(dhcpIpAddresses, computing.DhcpIpAddressSet{
			IpAddress: &ips[i],
		})
	}
}

func (c *FakeCloud) GetDhcpStatus(ctx context.Context, networkId, routerId string) (
	[]computing.IpAddressPoolSet, []computing.DhcpIpAddressSet, error) {
	c.Actions = append(c.Actions, "GetDhcpStatus/" + networkId + "/" + routerId)
	pools := []computing.IpAddressPoolSet{
		computing.IpAddressPoolSet{
			StartIpAddress: &poolStart,
			StopIpAddress: &poolStop,
		},
	}
	return pools, dhcpIpAddresses, nil
}

// rdb

var dbs []rdb.DBInstance

func initDB(ip, networkId string) {
	dbs = []rdb.DBInstance{}
	if ip != "" {
		dbs = append(dbs, rdb.DBInstance{
			NiftyNetworkId: &networkId,
			NiftyMasterPrivateAddress: &ip,
		})
	}
}

func (c *FakeCloud) ListRdbInstances(ctx context.Context) ([]rdb.DBInstance, error) {
	c.Actions = append(c.Actions, "ListRdbInstances")
	return dbs, nil
}
