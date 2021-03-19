package cloud

import (
	"fmt"
	"strings"
	"strconv"
	"os"

	"golang.org/x/net/context"

        "github.com/nifcloud/nifcloud-sdk-go/nifcloud"
	"github.com/aws/aws-sdk-go-v2/aws/awserr"
        "github.com/nifcloud/nifcloud-sdk-go/service/computing"
        "github.com/nifcloud/nifcloud-sdk-go/service/nas"

	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/util"
)

type Cloudiface interface {
	GetNasInstance(ctx context.Context, name string) (*nas.NASInstance, error)
	ListNasInstances(ctx context.Context) ([]nas.NASInstance, error)
	CreateNasInstance(ctx context.Context, n *nas.CreateNASInstanceInput) (*nas.NASInstance, error)
	ModifyNasInstance(ctx context.Context, name string) (*nas.NASInstance, error)
	DeleteNasInstance(ctx context.Context, name string) error
	GenerateVolumeIdFromNasInstance(obj *nas.NASInstance) string
	GetNasInstanceFromVolumeId(ctx context.Context, id string) (*nas.NASInstance, error)
	GetNasSecurityGroup(ctx context.Context, name string) (*nas.NASSecurityGroup, error)
	CreateNasSecurityGroup(ctx context.Context, sc *nas.CreateNASSecurityGroupInput) (*nas.NASSecurityGroup, error)
	AuthorizeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error)
	RevokeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error)
}

type Cloud struct {
        //Session *session.Session
	Nas *nas.Client
	Computing *computing.Client
	Region string
}

//type NasInstance struct {
//	nas.NASInstance
//}

func NewCloud(region string) (*Cloud, error) {

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
		Nas: nas.New(cfg),
		Computing: computing.New(cfg),
		Region: region,
	}, nil
}

// NAS capacity string
func getAllocatedStorage(capBytes int64, instanceType int64) *int64 {

	var allocatedStorage int64
	allocatedStorage = 100
	if instanceType == 1 {
		allocatedStorage = 1000
	}

	for i := 1; i < 10; i++ {
		if instanceType == 1 {
			allocatedStorage = int64(i)*1000
		} else {
			allocatedStorage = int64(i)*100
		}
		if util.GbToBytes(allocatedStorage) >= capBytes {
			break
		}
	}

	return &allocatedStorage
}

// CreateVolume parameters
func GenerateNasInstanceInput(name string, capBytes int64, params map[string]string) (*nas.CreateNASInstanceInput, error) {
	// Set default parameters
	var instanceType int64
	instanceType = 0
	zone := "east-11"
	network := "default"
	protocol := "nfs"
	var err error

	// Validate parameters (case-insensitive).
	for k, v := range params {
		switch strings.ToLower(k) {
		case "instancetype":
			instanceType, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid parameter %q", k)
			}
		case "zone":
			zone = v
		case "networkid":
			network = v
		case "reservedipv4cidr", "capacityparinstancegib", "shared":
			// allowed
		default:
			return nil, fmt.Errorf("invalid parameter %q", k)
		}
	}
	return &nas.CreateNASInstanceInput{
		AllocatedStorage: getAllocatedStorage(capBytes, instanceType),
		AvailabilityZone: &zone,
		//MasterPrivateAddress: must set after
		NASInstanceIdentifier: &name,
		NASInstanceType: &instanceType,
		//NASSecurityGroups: must set after
		NetworkId: &network,
		Protocol: &protocol,
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

func CompareNasInstanceWithInput(n *nas.NASInstance, in *nas.CreateNASInstanceInput) error {
	mismatches := []string{}
	if n.NASInstanceType == nil || *n.NASInstanceType != *in.NASInstanceType {
		mismatches = append(mismatches, "NASInstanceType")
	}
	allocatedStorage, _ := strconv.ParseInt(*n.AllocatedStorage, 10, 64)
	if allocatedStorage != *in.AllocatedStorage {
		mismatches = append(mismatches, "AllocatedStorage")
	}
	if n.NetworkId == nil || *n.NetworkId != *in.NetworkId {
		mismatches = append(mismatches, "NetworkId")
	}
	if n.AvailabilityZone == nil || *n.AvailabilityZone != *in.AvailabilityZone {
		mismatches = append(mismatches, "AvailabilityZone")
	}
	if len(n.NASSecurityGroups) != len(in.NASSecurityGroups) {
		mismatches = append(mismatches, "Number of NASSecurityGroups")
		for i := 0; i < len(n.NASSecurityGroups); i++ {
			if *n.NASSecurityGroups[i].NASSecurityGroupName != in.NASSecurityGroups[i] {
				mismatches = append(mismatches, "NASSecurityGroupName")
			}
		}
	}

	if len(mismatches) > 0 {
		return fmt.Errorf("instance %v already exists but doesn't match expected: %+v", n.NASInstanceIdentifier, mismatches)
	}
	return nil
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
