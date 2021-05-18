package cloud

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"golang.org/x/net/context"

	"github.com/aws/aws-sdk-go-v2/aws/awserr"
	"github.com/nifcloud/nifcloud-sdk-go/nifcloud"
	"github.com/nifcloud/nifcloud-sdk-go/service/computing"
	"github.com/nifcloud/nifcloud-sdk-go/service/nas"
	"github.com/nifcloud/nifcloud-sdk-go/service/hatoba"
	"github.com/nifcloud/nifcloud-sdk-go/service/rdb"
)

type Interface interface {
	// nas
	GetNasInstance(ctx context.Context, name string) (*nas.NASInstance, error)
	ListNasInstances(ctx context.Context) ([]nas.NASInstance, error)
	CreateNasInstance(ctx context.Context, n *nas.CreateNASInstanceInput) (*nas.NASInstance, error)
	ModifyNasInstance(ctx context.Context, name string) (*nas.NASInstance, error)
	ChangeNasInstanceSecurityGroup(ctx context.Context, name, sgname string) (*nas.NASInstance, error)
	DeleteNasInstance(ctx context.Context, name string) error
	GenerateVolumeIdFromNasInstance(obj *nas.NASInstance) string
	GetNasInstanceFromVolumeId(ctx context.Context, id string) (*nas.NASInstance, error)
	GetNasSecurityGroup(ctx context.Context, name string) (*nas.NASSecurityGroup, error)
	CreateNasSecurityGroup(ctx context.Context, sc *nas.CreateNASSecurityGroupInput) (*nas.NASSecurityGroup, error)
	AuthorizeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error)
	RevokeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error)
	// hatoba
	ListClusters(ctx context.Context) ([]hatoba.Cluster, error)
	// computing
	ListInstances(ctx context.Context) ([]computing.InstancesSet, error)
	GetPrivateLan(ctx context.Context, networkId string) (*computing.PrivateLanSet, error)
	GetDhcpStatus(ctx context.Context, networkId, routerId string) ([]computing.IpAddressPoolSet, []computing.DhcpIpAddressSet, error)
	// rdb
	ListRdbInstances(ctx context.Context) ([]rdb.DBInstance, error)
}

type Cloud struct {
	//Session *session.Session
	Nas       *nas.Client
	Computing *computing.Client
	Hatoba    *hatoba.Client
	Rdb       *rdb.Client
	Region    string
	DevEp     string
}

func NewCloud(region, devcloudep string) (*Cloud, error) {

	// Get credentials
	accesskey := os.Getenv("AWS_ACCESS_KEY_ID")
	if accesskey == "" {
		return nil, fmt.Errorf("Cannot set accesskey from env.")
	}
	secretkey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if secretkey == "" {
		return nil, fmt.Errorf("Cannot set secretkey from env.")
	}

	// Create config with credentials and region.
	cfg := nifcloud.NewConfig(accesskey, secretkey, region)

	return &Cloud{
		//Session: sess,
		Nas:       nas.New(cfg),
		Computing: computing.New(cfg),
		Hatoba:    hatoba.New(cfg),
		Rdb:       rdb.New(cfg),
		Region:    region,
		DevEp:     devcloudep,
	}, nil
}

func (c *Cloud) GetNasInstance(ctx context.Context, name string) (*nas.NASInstance, error) {
	// Call describe NAS Instances
	req := c.Nas.DescribeNASInstancesRequest(
		&nas.DescribeNASInstancesInput{NASInstanceIdentifier: &name},
	)

	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}

	return &output.NASInstances[0], nil
}

func IsNotFoundErr(err error) bool {
	if awsErr, ok := err.(awserr.Error); ok {
		return strings.Contains(awsErr.Code(), "NotFound")
	}
	return false
}

func (c *Cloud) ListNasInstances(ctx context.Context) ([]nas.NASInstance, error) {
	// Call describe NAS Instances
	req := c.Nas.DescribeNASInstancesRequest(&nas.DescribeNASInstancesInput{})

	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}

	return output.NASInstances, nil
}

func (c *Cloud) CreateNasInstance(ctx context.Context, n *nas.CreateNASInstanceInput) (*nas.NASInstance, error) {
	// Call create NAS Instances
	req := c.Nas.CreateNASInstanceRequest(n)

	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	return output.NASInstance, nil
}

func (c *Cloud) ModifyNasInstance(ctx context.Context, name string) (*nas.NASInstance, error) {
	// Call modify NAS Instance to set NoRootSquash=true
	no_root_squash := "true"
	req := c.Nas.ModifyNASInstanceRequest(
		&nas.ModifyNASInstanceInput{NASInstanceIdentifier: &name, NoRootSquash: &no_root_squash},
	)

	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	return output.NASInstance, nil
}

func (c *Cloud) ChangeNasInstanceSecurityGroup(ctx context.Context, name, sgname string) (*nas.NASInstance, error) {
	// Call modify NAS Instance to set NasSecurityGroups
	sgs := []string{sgname}
	req := c.Nas.ModifyNASInstanceRequest(
		&nas.ModifyNASInstanceInput{NASInstanceIdentifier: &name, NASSecurityGroups: sgs},
	)

	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	return output.NASInstance, nil
}

func (c *Cloud) DeleteNasInstance(ctx context.Context, name string) error {
	// Call delete NAS Instances
	req := c.Nas.DeleteNASInstanceRequest(
		&nas.DeleteNASInstanceInput{NASInstanceIdentifier: &name},
	)

	_, err := req.Send(ctx)
	if err != nil {
		return err
	}
	return nil
}

// getVolumeIdFromFileInstance generates an id to uniquely identify the nifcloud NAS.
// This id is used for volume deletion.
func (c *Cloud) GenerateVolumeIdFromNasInstance(obj *nas.NASInstance) string {
	idElements := make([]string, 0)
	idElements = append(idElements, c.Region)
	idElements = append(idElements, *obj.NASInstanceIdentifier)
	return strings.Join(idElements, "/")
}

func (c *Cloud) GetNasInstanceFromVolumeId(ctx context.Context, id string) (*nas.NASInstance, error) {
	tokens := strings.Split(id, "/")
	if len(tokens) != 2 {
		return nil, fmt.Errorf("volume id %q unexpected format: got %v tokens", id, len(tokens))
	}

	return c.GetNasInstance(ctx, tokens[1])
}

// NasSecurityGroups functions

func (c *Cloud) GetNasSecurityGroup(ctx context.Context, name string) (*nas.NASSecurityGroup, error) {
	// Call describe NAS Security Groups
	req := c.Nas.DescribeNASSecurityGroupsRequest(
		&nas.DescribeNASSecurityGroupsInput{NASSecurityGroupName: &name},
	)

	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	return &output.NASSecurityGroups[0], nil
}

func (c *Cloud) CreateNasSecurityGroup(ctx context.Context, sc *nas.CreateNASSecurityGroupInput) (*nas.NASSecurityGroup, error) {
	// Call create NAS Instances
	req := c.Nas.CreateNASSecurityGroupRequest(sc)

	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	return output.NASSecurityGroup, nil
}

func (c *Cloud) AuthorizeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error) {
	// Call authorize NAS Security Groups ingress
	req := c.Nas.AuthorizeNASSecurityGroupIngressRequest(
		&nas.AuthorizeNASSecurityGroupIngressInput{NASSecurityGroupName: &name, CIDRIP: &cidrip},
	)

	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	return output.NASSecurityGroup, nil
}

func (c *Cloud) RevokeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error) {
	// Call revoke NAS Security Groups ingress
	req := c.Nas.RevokeNASSecurityGroupIngressRequest(
		&nas.RevokeNASSecurityGroupIngressInput{NASSecurityGroupName: &name, CIDRIP: &cidrip},
	)

	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	return output.NASSecurityGroup, nil
}

// hatoba

func (c *Cloud) ListClusters(ctx context.Context) ([]hatoba.Cluster, error) {
	// Call list clusters
	req := c.Hatoba.ListClustersRequest(&hatoba.ListClustersInput{})

	// Set dev cloud endpoint
	if c.DevEp != "" {
		u, err := url.Parse(c.DevEp)
		if err != nil {
			return nil, err
		}
		req.Request.HTTPRequest.URL.Host = u.Host
		req.Request.HTTPRequest.URL.Scheme = u.Scheme
	}
	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	return output.Clusters, nil
}

// computing

func (c *Cloud) ListInstances(ctx context.Context) ([]computing.InstancesSet, error) {
	// Call describe Instances
	req := c.Computing.DescribeInstancesRequest(&computing.DescribeInstancesInput{})
	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	instances := []computing.InstancesSet{}
	for _, r := range output.ReservationSet {
		instances = append(instances, r.InstancesSet...)
	}
	return instances, nil
}

func (c *Cloud) GetPrivateLan(ctx context.Context, networkId string) (*computing.PrivateLanSet, error) {
	// Call NiftyDescribePrivateLans
	req := c.Computing. NiftyDescribePrivateLansRequest(&computing.NiftyDescribePrivateLansInput{
		NetworkId: []string{networkId},
	})
	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	return &output.PrivateLanSet[0], nil
}

func (c *Cloud) GetDhcpStatus(ctx context.Context, networkId, routerId string) (
	[]computing.IpAddressPoolSet, []computing.DhcpIpAddressSet, error) {

	// Call NiftyDescribeDhcpStatus
	req := c.Computing. NiftyDescribeDhcpStatusRequest(&computing.NiftyDescribeDhcpStatusInput{
		RouterId: &routerId,
	})
	output, err := req.Send(ctx)
	if err != nil {
		return nil, nil, err
	}
	pools := []computing.IpAddressPoolSet{}
	ips := []computing.DhcpIpAddressSet{}
	for _, i := range output.DhcpStatusInformationSet {
		if *i.NetworkId != networkId {
			continue
		}
		pools = append(pools, i.DhcpIpAddressInformation.IpAddressPoolSet...)
		for _, ip := range i.DhcpIpAddressInformation.DhcpIpAddressSet {
			if pstr(ip.LeaseType) == "static" {
				ips = append(ips, ip)
			}
		}
	}
	return pools, ips, nil
}

// rdb

func (c *Cloud) ListRdbInstances(ctx context.Context) ([]rdb.DBInstance, error) {
	// Call list DB Instances
	req := c.Rdb.DescribeDBInstancesRequest(&rdb.DescribeDBInstancesInput{})
	output, err := req.Send(ctx)
	if err != nil {
		return nil, err
	}
	return output.DBInstances, nil
}

// util

func pstr(pstr *string) string {
	if pstr == nil {
		return ""
	}
	return *pstr
}
