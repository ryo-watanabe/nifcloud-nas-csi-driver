package driver

import (
	"fmt"
	"flag"
	"reflect"
	"strings"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
	"github.com/aws/aws-sdk-go-v2/aws/awserr"
	"github.com/nifcloud/nifcloud-sdk-go/service/nas"
	"github.com/ryo-watanabe/nfcl-nas-csi-driver/pkg/util"
	corev1 "k8s.io/api/core/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
)

func TestCreateVolume(t *testing.T) {

	rand.Seed(1)
	sharedSuffix := rand.String(5)

	cases := []struct {
		testname string
		req *csi.CreateVolumeRequest
		res *csi.CreateVolumeResponse
		errmsg string
		shared bool
	}{
		{
			testname: "valid volume",
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 10 * util.Gb, 0, "192.168.100.0/28"),
			res: initCreateVolumeResponse("testregion/pvc-TESTPVCUID", 100 * util.Gb, "192.168.100.0/24", ""),
		},
		{
			testname: "valid shared volume",
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 10 * util.Gb, 0, "192.168.100.0/28"),
			res: initCreateVolumeResponse("testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID", 10 * util.Gb, "192.168.100.0/24", "pvc-TESTPVCUID"),
			shared: true,
		},
	}

	flag.Set("logtostderr", "true")
	flag.Lookup("v").Value.Set("4")
	flag.Parse()

	for _, c := range(cases) {
		ctl := initTestController(t)
		if c.shared {
			c.req.Parameters["shared"] = "true"
			c.req.Parameters["capacityParInstanceGiB"] = "500"
		}
		res, err := ctl.CreateVolume(context.TODO(), c.req)
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("Not expected error in case %s : %s", c.testname, err.Error())
			} else if !reflect.DeepEqual(res, c.res) {
				t.Errorf("Expected response: %v but got: %v in case %s", c.res, res, c.testname)
			}
		} else {
			if err == nil {
				t.Errorf("Expected error: %s not occured in case %s", c.errmsg, c.testname)
			} else if err.Error() != c.errmsg {
				t.Errorf("Expected error: %s but got: %s in case %s", c.errmsg, err.Error(), c.testname)
			}
		}
	}
}

func initCreateVolumeResquest(name string, capReq, capLim int64, cidr string) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name: name,
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
				},
			},
		},
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: capReq,
			LimitBytes: capLim,
		},
		Parameters: map[string]string{
			"reservedIpv4Cidr": cidr,
		},
	}
}

func initCreateVolumeResponse(volumeID string, cap int64, ip, sourcePath string) *csi.CreateVolumeResponse {
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			CapacityBytes: cap,
			VolumeId:      volumeID,
			VolumeContext: map[string]string{
				attrIp:     ip,
				attrSourcePath: sourcePath,
			},
		},
	}
}

func initTestController(t *testing.T) csi.ControllerServer {
	// test cloud
	cloud := newFakeCloud()
	// test k8s
	kubeobjects := []runtime.Object{}
	kubeobjects = append(kubeobjects, newNamespace("kube-system", "TESTCLUSTERUID"))
	kubeobjects = append(kubeobjects, newPVC("testpvc", "100Gi", "TESTPVCUID"))
	kubeClient := k8sfake.NewSimpleClientset(kubeobjects...)
	// all jobs are created with status Complete
	kubeClient.Fake.PrependReactor("create", "jobs", func(action core.Action) (bool, runtime.Object, error) {
		obj := action.(core.CreateAction).GetObject()
		job, _ := obj.(*batchv1.Job)
		job.Status.Conditions = []batchv1.JobCondition{
			batchv1.JobCondition{Type: "Complete"},
		}
		return false, job, nil
	})
	// init random
	rand.Seed(1)

	driver := initTestDriver(t, cloud, kubeClient, true, false)
	return driver.cs
}

func newPVC(name, requestStorage, uid string) *corev1.PersistentVolumeClaim {
	q, _ := resource.ParseQuantity(requestStorage)
	return &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
			UID: types.UID(uid),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{"storage":q},
			},
		},
	}
}

func newNamespace(name, uid string) *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID: types.UID(uid),
		},
	}
}

type FakeCloud struct {
	NasInstances []nas.NASInstance
	NasSecurityGroups []nas.NASSecurityGroup
	waitCnt int
}

// Fake Cloud

func newFakeCloud() *FakeCloud {
	return &FakeCloud{}
}

var (
	statusAvailable = "available"
	statusCreating = "creating"
	statusModifying = "modifying"
	valueTrue = "true"
	valueFalse = "false"
)

func (c *FakeCloud) GetNasInstance(ctx context.Context, name string) (*nas.NASInstance, error) {
	for i, n := range(c.NasInstances) {
		if *n.NASInstanceIdentifier == name {
			c.waitCnt--
			if c.waitCnt <= 0 {
				c.NasInstances[i].NASInstanceStatus = &statusAvailable
				c.waitCnt = 0
			}
			return &c.NasInstances[i], nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}

func (c *FakeCloud) ListNasInstances(ctx context.Context) ([]nas.NASInstance, error) {
	return c.NasInstances, nil
}

func (c *FakeCloud) CreateNasInstance(ctx context.Context, in *nas.CreateNASInstanceInput) (*nas.NASInstance, error) {
	storage := fmt.Sprintf("%d", *in.AllocatedStorage)
	n := nas.NASInstance{
		AllocatedStorage: &storage,
		AvailabilityZone: in.AvailabilityZone,
		NASInstanceIdentifier: in.NASInstanceIdentifier,
		NASInstanceType: in.NASInstanceType,
		NASSecurityGroups: []nas.NASSecurityGroup{
			nas.NASSecurityGroup{NASSecurityGroupName: &in.NASSecurityGroups[0]},
		},
		Endpoint: &nas.Endpoint{PrivateAddress: in.MasterPrivateAddress},
		NetworkId: in.NetworkId,
		Protocol: in.Protocol,
		NASInstanceStatus: &statusCreating,
		NoRootSquash: &valueFalse,
	}
	c.NasInstances = append(c.NasInstances, n)
	c.waitCnt = 2
	return &n, nil
}

func (c *FakeCloud) ModifyNasInstance(ctx context.Context, name string) (*nas.NASInstance, error) {
	for i, n := range(c.NasInstances) {
		if *n.NASInstanceIdentifier == name {
			c.NasInstances[i].NASInstanceStatus = &statusModifying
			c.NasInstances[i].NoRootSquash = &valueTrue
			c.waitCnt = 2
			return &c.NasInstances[i], nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}

func (c *FakeCloud) DeleteNasInstance(ctx context.Context, name string) error {
	return nil
}

func (c *FakeCloud) GenerateVolumeIdFromNasInstance(obj *nas.NASInstance) string {
	return "testregion/" + *obj.NASInstanceIdentifier
}

func (c *FakeCloud) GetNasInstanceFromVolumeId(ctx context.Context, id string) (*nas.NASInstance, error) {
	tokens := strings.Split(id, "/")
	if len(tokens) != 2 {
		return nil, fmt.Errorf("volume id %q unexpected format: got %v tokens", id, len(tokens))
	}

	return c.GetNasInstance(ctx, tokens[1])
}

func (c *FakeCloud) GetNasSecurityGroup(ctx context.Context, name string) (*nas.NASSecurityGroup, error) {
	for _, g := range(c.NasSecurityGroups) {
		if *g.NASSecurityGroupName == name {
			return &g, nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}

func (c *FakeCloud) CreateNasSecurityGroup(ctx context.Context, sc *nas.CreateNASSecurityGroupInput) (*nas.NASSecurityGroup, error) {
	g := nas.NASSecurityGroup{
		AvailabilityZone: sc.AvailabilityZone,
		NASSecurityGroupName: sc.NASSecurityGroupName,
	}
	return &g, nil
}

func (c *FakeCloud) AuthorizeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error) {
	for _, g := range(c.NasSecurityGroups) {
		if *g.NASSecurityGroupName == name {
			// TODO: authorize
			return &g, nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}

func (c *FakeCloud) RevokeCIDRIP(ctx context.Context, name, cidrip string) (*nas.NASSecurityGroup, error) {
	for _, g := range(c.NasSecurityGroups) {
		if g.NASSecurityGroupName == &name {
			// TODO: revoke
			return &g, nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}
