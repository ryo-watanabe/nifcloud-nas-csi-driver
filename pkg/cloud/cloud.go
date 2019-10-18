package cloud

import (
	"fmt"
	"strings"
	"strconv"

	"golang.org/x/net/context"

        "github.com/alice02/nifcloud-sdk-go/nifcloud"
        "github.com/alice02/nifcloud-sdk-go/nifcloud/credentials"
        "github.com/alice02/nifcloud-sdk-go/nifcloud/session"
	"github.com/alice02/nifcloud-sdk-go/nifcloud/awserr"
        "github.com/alice02/nifcloud-sdk-go/service/computing"
        "github.com/alice02/nifcloud-sdk-go/service/nas"

	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/util"
)

type Cloud struct {
        Session *session.Session
	Nas *nas.Nas
	Computing *computing.Computing
	Region string
}

//type NasInstance struct {
//	nas.NASInstance
//}

func NewCloud(region string) (*Cloud, error) {

        // Set session from credentials in env.
        sess, err := session.NewSession(&nifcloud.Config{
                                Region: nifcloud.String(region),
                                Credentials: credentials.NewEnvCredentials(),
                        })
        if err != nil {
                return nil, fmt.Errorf("failed to initialize session: %v", err)
        }

	return &Cloud{
                Session: sess,
		Nas: nas.New(sess),
		Computing: computing.New(sess),
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
	securityGroup := "default"
	zone := "east-11"
	network := "default"
	protocol := "nfs"

	// Validate parameters (case-insensitive).
	for k, v := range params {
		switch strings.ToLower(k) {
		case "instancetype":
			instanceType, _ = strconv.ParseInt(v, 10, 64)
		case "zone":
			zone = v
		case "securitygroup":
			securityGroup = v
		case "networkid":
			network = v
		case "reservedipv4cidr":
			// must set after
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
		NASSecurityGroups: []*string{&securityGroup},
		NetworkId: &network,
		Protocol: &protocol,
	}, nil
}

func (c *Cloud) GetNasInstance(ctx context.Context, name string) (*nas.NASInstance, error) {
	// Call describe NAS Instances
	output, err := c.Nas.DescribeNASInstancesWithContext(
		ctx,
		&nas.DescribeNASInstancesInput{NASInstanceIdentifier: &name},
	)

	if err != nil {
		return nil, err
	}

	return output.NASInstances[0], nil
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
			if n.NASSecurityGroups[i].NASSecurityGroupName != in.NASSecurityGroups[i] {
				mismatches = append(mismatches, "NASSecurityGroupName")
			}
		}
	}

	if len(mismatches) > 0 {
		return fmt.Errorf("instance %v already exists but doesn't match expected: %+v", n.NASInstanceIdentifier, mismatches)
	}
	return nil
}

func (c *Cloud) ListNasInstances(ctx context.Context) ([]*nas.NASInstance, error) {
	// Call describe NAS Instances
	output, err := c.Nas.DescribeNASInstancesWithContext(ctx, &nas.DescribeNASInstancesInput{})

	if err != nil {
		return nil, err
	}

	return output.NASInstances, nil
}

func (c *Cloud) CreateNasInstance(ctx context.Context, n *nas.CreateNASInstanceInput) (*nas.NASInstance, error) {
	// Call create NAS Instances
	output, err := c.Nas.CreateNASInstanceWithContext(ctx, n)

	if err != nil {
		return nil, err
	}
	return output.NASInstance, nil
}

func (c *Cloud) ModifyNasInstance(ctx context.Context, name string) (*nas.NASInstance, error) {
	// Call modify NAS Instance to set NoRootSquash=true
	no_root_squash := "true"
	output, err := c.Nas.ModifyNASInstanceWithContext(
		ctx,
		&nas.ModifyNASInstanceInput{NASInstanceIdentifier: &name, NoRootSquash: &no_root_squash},
	)

	if err != nil {
		return nil, err
	}
	return output.NASInstance, nil
}

func (c *Cloud) DeleteNasInstance(ctx context.Context, name string) error {
	// Call delete NAS Instances
	_, err := c.Nas.DeleteNASInstanceWithContext(
		ctx,
		&nas.DeleteNASInstanceInput{NASInstanceIdentifier: &name},
	)

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
