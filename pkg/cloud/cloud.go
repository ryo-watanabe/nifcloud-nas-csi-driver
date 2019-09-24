package cloud

import (
	"fmt"
	"strings"

        "github.com/alice02/nifcloud-sdk-go/nifcloud"
        "github.com/alice02/nifcloud-sdk-go/nifcloud/credentials"
        "github.com/alice02/nifcloud-sdk-go/nifcloud/session"
	"github.com/alice02/nifcloud-sdk-go/nifcloud/awserr"
        "github.com/alice02/nifcloud-sdk-go/service/computing"
        "github.com/alice02/nifcloud-sdk-go/service/nas"
)

type Cloud struct {
        Session session.Session
	Nas nas.Nas
	Computing computing.Computing
	Region string
}

//type NasInstance struct {
//	nas.NASInstance
//}

func NewCloud(region string) (*Cloud, error) {

        // Set session from credentials in env.
        sess, err := session.NewSession(
                        &nifcloud.Config{
                                Region: nifcloud.String(region),
                                Credentials: credentials.NewEnvCredentials(),
                        }
                )
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
func getAllocatedStorage(capBytes int64, instanceType int) int64 {

	allocatedStorage := 100
	if instanceType == 1 {
		allocatedStorage = 1000
	}

	for i := 1; i < 10; i++ {
		if instanceType == 1 {
			allocatedStorage = i*1000
		} else {
			allocatedStorage = i*100
		}
		if allocatedStorage > capBytes {
			break
		}
	}

	return int64(allocatedStorage)
}

// CreateVolume parameters
func GenerateNasInstanceInput(name string, capBytes int64, params map[string]string) (*nas.CreateNASInstanceInput, error) {
	// Set default parameters
	instanceType := 0
	securityGroup := "default"
	zone := "east-11"
	network := "default"

	// Validate parameters (case-insensitive).
	for k, v := range params {
		switch strings.ToLower(k) {
		case "instancetype":
			instanceType = v
		case "zone":
			zone = v
		case "securitygroup":
			securityGroup = v
		case "networkid":
			network = v
		default:
			return nil, fmt.Errorf("invalid parameter %q", k)
		}
	}
	return &nas.NASInstance{
		AllocatedStorage: getAllocatedStorage(capBytes, instanceType),
		AvailabilityZone: zone,
		//MasterPrivateAddress: privateAddress
		NASInstanceIdentifier: name,
		NASInstanceType: instanceType,
		NASSecurityGroups: {nas.NASSecurityGroup{NASSecurityGroupName: securityGroup}},
		NetworkId: network,
		Protocol: "nfs"
	}, nil
}

func (c *Cloud) GetNasInstance(ctx context.Context, name string) (*nas.NASInstance, error) {
	// Call describe NAS Instances
	output, err := c.Nas.describeNASInstancesWithContext(
		ctx,
		&nas.describeNASInstancesInput{NASInstanceIdentifier: &name},
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

func CompareNasInstances(n *nas.NASInstance, in *nas.CreateNASInstanceInput) error {
	mismatches := []string{}
	if n.NASInstanceType != in.NASInstanceType {
		mismatches = append(mismatches, "NASInstanceType")
	}
	if strconv.ParseInt(n.AllocatedStorage, 10, 64) != in.AllocatedStorage {
		mismatches = append(mismatches, "AllocatedStorage")
	}
	if n.NetworkId != in.NetworkId {
		mismatches = append(mismatches, "NetworkId")
	}
	if n.AvailabilityZone != in.AvailabilityZone {
		mismatches = append(mismatches, "AvailabilityZone")
	}
	if len(n.NASSecurityGroups) != len(in.NASSecurityGroups) {
		mismatches = append(mismatches, "Number of NASSecurityGroups")
		for i := 0; i < len(n.NASSecurityGroups); i++ {
			if n.NASSecurityGroups[i].NASSecurityGroupName != in.NASSecurityGroups[i].NASSecurityGroupName {
				mismatches = append(mismatches, "NASSecurityGroupName")
			}
		}
	}

	if len(mismatches) > 0 {
		return fmt.Errorf("instance %v already exists but doesn't match expected: %+v", n.Name, mismatches)
	}
	return nil
}

func (c *Cloud) ListNasInstance(ctx context.Context) ([]*nas.NASInstance, error) {
	// Call describe NAS Instances
	output, err := c.Nas.describeNASInstancesWithContext(ctx, &nas.describeNASInstancesInput{})

	if err != nil {
		return nil, err
	}

	return output.NASInstances, nil
}

func (c *Cloud) CreateNasInstance(ctx context.Context, n *nas.CreateNASInstanceInput) (*nas.NASInstance, error) {
	// Call create NAS Instances
	output, err := c.Nas.createNASInstanceWithContext(ctx, n)

	if err != nil {
		return nil, err
	}
	return output.NASInstance, nil
}

func (c *Cloud) DeleteNasInstance(ctx context.Context, name string) (*nas.NASInstance, error) {
	// Call delete NAS Instances
	output, err := c.Nas.deleteNASInstanceWithContext(
		ctx,
		&nas.deleteNASInstanceInput{NASInstanceIdentifier: &name},
	)

	if err != nil {
		return nil, err
	}
	return output.NASInstance, nil
}

// getVolumeIdFromFileInstance generates an id to uniquely identify the nifcloud NAS.
// This id is used for volume deletion.
func (c *Cloud) GenerateVolumeIdFromNasInstance(obj *nas.NASInstance) string {
	idElements := make([]string, 0)
	idElements = append(idElements, c.Region)
	idElements = append(idElements, obj.NASInstanceIdentifier)
	return strings.Join(idElements, "/")
}

func (c *Cloud) getNasInstanceFromVolumeId(id string) (*nas.NASInstance, error) {
	tokens := strings.Split(id, "/")
	if len(tokens) != 2 {
		return nil, fmt.Errorf("volume id %q unexpected format: got %v tokens", id, len(tokens))
	}

	return c.GetNasInstance(ctx, tokens[1])
}
