package driver

import (
	"fmt"

	"golang.org/x/net/context"
	"github.com/aws/aws-sdk-go-v2/aws/awserr"
	"github.com/nifcloud/nifcloud-sdk-go/service/computing"
	"github.com/nifcloud/nifcloud-sdk-go/service/hatoba"
	"github.com/nifcloud/nifcloud-sdk-go/service/rdb"
)

// FakeCloud implementations

// hatoba
func (c *FakeCloud) ListClusters(ctx context.Context) ([]hatoba.Cluster, error) {
	return []hatoba.Cluster{}, nil
}

// computing
func (c *FakeCloud) ListInstances(ctx context.Context) ([]computing.InstancesSet, error) {
	return []computing.InstancesSet{}, nil
}

func (c *FakeCloud) GetPrivateLan(ctx context.Context, networkId string) (*computing.PrivateLanSet, error) {
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("Private Lan (networkId=%s) not found", networkId))
}

func (c *FakeCloud) GetDhcpStatus(ctx context.Context, networkId, routerId string) (
	[]computing.IpAddressPoolSet, []computing.DhcpIpAddressSet, error) {
	return []computing.IpAddressPoolSet{}, []computing.DhcpIpAddressSet{}, nil
}

// rdb
func (c *FakeCloud) ListRdbInstances(ctx context.Context) ([]rdb.DBInstance, error) {
	return []rdb.DBInstance{}, nil
}
