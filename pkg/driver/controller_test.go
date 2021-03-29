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
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
)

const (
	volumeSize = 100 * util.Gb
	volumeQnty = "100Gi"
)

func TestCreateVolume(t *testing.T) {

	rand.Seed(1)
	sharedSuffix := rand.String(5)

	cases := map[string]struct {
		obj []runtime.Object
		pre []*csi.CreateVolumeRequest
		req *csi.CreateVolumeRequest
		res *csi.CreateVolumeResponse
		errmsg string
	}{
		"valid volume":{
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 10 * util.Gb, 0, "192.168.100.0/28", false),
			res: initCreateVolumeResponse("testregion/pvc-TESTPVCUID", 100 * util.Gb, "192.168.100.0", ""),
		},
		"volume with no name":{
			req: initCreateVolumeResquest("", 0, 0, "0.0.0.0/32", false),
			errmsg: "CreateVolume name must be provided",
		},
		"unsupported access mode":{
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 0, 0, "0.0.0.0/32", false),
			errmsg: "driver does not support access mode",
		},
		"take neighbor ip":{
			obj: []runtime.Object{
				newPVC("testpvc", volumeQnty, "TESTPVCUID"),
				newPVC("testpvc-pre", volumeQnty, "TESTPVCUID-pre"),
			},
			pre: []*csi.CreateVolumeRequest{
				initCreateVolumeResquest("pvc-TESTPVCUID-pre", volumeSize, 0, "192.168.100.0/28", false),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", volumeSize, 0, "192.168.100.0/28", false),
			res: initCreateVolumeResponse("testregion/pvc-TESTPVCUID", volumeSize, "192.168.100.1", ""),
		},
		"valid shared volume":{
			obj: []runtime.Object{
				newPVC("testpvc", volumeQnty, "TESTPVCUID"),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", volumeSize, 0, "192.168.100.0/28", true),
			res: initCreateVolumeResponse("testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID", volumeSize, "192.168.100.0", "pvc-TESTPVCUID"),
		},
		"room shared volume":{
			obj: []runtime.Object{
				newPVC("testpvc-pre", volumeQnty, "TESTPVCUID-pre"),
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			pre: []*csi.CreateVolumeRequest{
				initCreateVolumeResquest("pvc-TESTPVCUID-pre", volumeSize, 0, "192.168.100.0/28", true),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 10 * util.Gb, 0, "192.168.100.0/28", true),
			res: initCreateVolumeResponse("testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID", 10 * util.Gb, "192.168.100.0", "pvc-TESTPVCUID"),
		},
	}

	// additional params for tests
	cases["unsupported access mode"].req.VolumeCapabilities[0].AccessMode = &csi.VolumeCapability_AccessMode{
		Mode: csi.VolumeCapability_AccessMode_UNKNOWN,
	}

	flag.Set("logtostderr", "true")
	flag.Lookup("v").Value.Set("4")
	flag.Parse()

	for name, c := range(cases) {
		t.Logf("====== Test case [%s] :", name)
		ctl, kubeClient := initTestController(t, c.obj)
		// pre existing volumes
		failed := false
		for _, v := range(c.pre) {
			err := createVolumeAndPV(ctl, v, kubeClient)
			if err != nil {
				t.Errorf("cannot create pre existing volume in case [%s] : %s", name, err.Error())
				failed = true
				break
			}
		}
		if failed {
			continue
		}
		// test the case
		res, err := ctl.CreateVolume(context.TODO(), c.req)
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
			} else if !reflect.DeepEqual(res, c.res) {
				t.Errorf("result not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.res, res)
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

func TestDeleteVolume(t *testing.T) {

	rand.Seed(1)
	sharedSuffix := rand.String(5)

	cases := map[string]struct {
		obj []runtime.Object
		pre []*csi.CreateVolumeRequest
		req *csi.DeleteVolumeRequest
		errmsg string
	}{
		"delete volume":{
			obj: []runtime.Object{
				newPVC("testpvc", volumeQnty, "TESTPVCUID"),
			},
			pre: []*csi.CreateVolumeRequest{
				initCreateVolumeResquest("pvc-TESTPVCUID", volumeSize, 0, "192.168.100.0/28", false),
			},
			req: &csi.DeleteVolumeRequest{VolumeId: "testregion/pvc-TESTPVCUID"},
		},
		"delete shared volume":{
			obj: []runtime.Object{
				newPVC("testpvc", volumeQnty, "TESTPVCUID"),
			},
			pre: []*csi.CreateVolumeRequest{
				initCreateVolumeResquest("pvc-TESTPVCUID", volumeSize, 0, "192.168.100.0/28", true),
			},
			req: &csi.DeleteVolumeRequest{VolumeId: "testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID"},
		},
	}

	flag.Set("logtostderr", "true")
	flag.Lookup("v").Value.Set("4")
	flag.Parse()

	for name, c := range(cases) {
		t.Logf("====== Test case [%s] :", name)
		ctl, kubeClient := initTestController(t, c.obj)
		// pre existing volumes
		failed := false
		for _, v := range(c.pre) {
			err := createVolumeAndPV(ctl, v, kubeClient)
			if err != nil {
				t.Errorf("cannot create pre-existing volume in case [%s] : %s", name, err.Error())
				failed = true
				break
			}
		}
		if failed {
			continue
		}
		// test the case
		_, err := ctl.DeleteVolume(context.TODO(), c.req)
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
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

func initCreateVolumeResquest(name string, capReq, capLim int64, cidr string, shared bool) *csi.CreateVolumeRequest {
	req := &csi.CreateVolumeRequest{
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
	if shared {
		req.Parameters["shared"] = "true"
		req.Parameters["capacityParInstanceGiB"] = "500"
	}
	return req
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

func createVolumeAndPV(ctl csi.ControllerServer, req *csi.CreateVolumeRequest, kubeClient kubernetes.Interface) error {
	res, err := ctl.CreateVolume(context.TODO(), req)
	if err != nil {
		return err
	}
	_, err = kubeClient.CoreV1().PersistentVolumes().Create(
		context.TODO(),
		newPV(req.Name, res.Volume.VolumeId, volumeQnty),
		metav1.CreateOptions{},
	)
	if err != nil {
		return err
	}
	return nil
}

func initTestController(t *testing.T, objects []runtime.Object) (csi.ControllerServer, kubernetes.Interface) {
	// test cloud
	cloud := newFakeCloud()
	// test k8s
	kubeobjects := []runtime.Object{}
	kubeobjects = append(kubeobjects, newNamespace("kube-system", "TESTCLUSTERUID"))
	kubeobjects = append(kubeobjects, objects...)
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
	return driver.cs, kubeClient
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

func newPV(name, volumeHandle, storage string) *corev1.PersistentVolume {
	q, _ := resource.ParseQuantity(storage)
	return &corev1.PersistentVolume{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolume"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					VolumeHandle: volumeHandle,
				},
			},
			Capacity: corev1.ResourceList{"storage":q},
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
	pIp := strings.SplitN(*in.MasterPrivateAddress, "/", 2)
	ip := pIp[0]
	n := nas.NASInstance{
		AllocatedStorage: &storage,
		AvailabilityZone: in.AvailabilityZone,
		NASInstanceIdentifier: in.NASInstanceIdentifier,
		NASInstanceType: in.NASInstanceType,
		NASSecurityGroups: []nas.NASSecurityGroup{
			nas.NASSecurityGroup{NASSecurityGroupName: &in.NASSecurityGroups[0]},
		},
		Endpoint: &nas.Endpoint{PrivateAddress: &ip},
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
