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

func TestCreateVolume(t *testing.T) {

	rand.Seed(1)
	sharedSuffix := rand.String(5)

	cases := map[string]struct {
		obj []runtime.Object
		pre []nas.NASInstance
		req *csi.CreateVolumeRequest
		res *csi.CreateVolumeResponse
		post []nas.NASInstance
		job_failed bool
		err_on_create bool
		err_after_wait bool
		errmsg string
	}{
		"valid volume 100Gi for request 10Gi":{
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 10 * util.Gb, 0, "192.168.100.0/28", false),
			res: initCreateVolumeResponse("testregion/pvc-TESTPVCUID", 100 * util.Gb, "192.168.100.0", ""),
			post: []nas.NASInstance{initNASInstance("pvc-TESTPVCUID", "192.168.100.0", 100)},
		},
		"volume with no name":{
			req: initCreateVolumeResquest("", 0, 0, "0.0.0.0/32", false),
			errmsg: "CreateVolume name must be provided",
		},
		"unsupported access mode":{
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 0, 0, "0.0.0.0/32", false),
			errmsg: "driver does not support access mode",
		},
		"invalid parameter":{
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 0, 0, "0.0.0.0/32", false),
			errmsg: "invalid parameter \"unknownparam\"",
		},
		"invalid shared capacity value":{
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 0, 0, "0.0.0.0/32", true),
			errmsg: "Invalid value in capacityParInstanceGiB: strconv.ParseInt: parsing \"500Gi\": invalid syntax",
		},
		"too big to share":{
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 1000 * util.Gb, 0, "0.0.0.0/32", true),
			errmsg: "Request capacity 1073741824000 is too big to share a NAS instance",
		},
		"volume id without uid":{
			req: initCreateVolumeResquest("somePVCName", 0, 0, "0.0.0.0/32", false),
			errmsg: "getting IP from PVC : Cannot split UID",
		},
		"pvc not found":{
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 0, 0, "0.0.0.0/32", false),
			errmsg: "PVC TESTPVCUID not found",
		},
		"ip not provided":{
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 0, 0, "", false),
			errmsg: "reservedIpv4Cidr must be provided",
		},
		"error on create":{
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 10 * util.Gb, 0, "10.100.0.0/28", false),
			err_on_create: true,
			errmsg: "NAS Instance pvc-TESTPVCUID status:unknown",
		},
		"error after wait":{
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 10 * util.Gb, 0, "10.100.0.0/28", false),
			err_after_wait: true,
			errmsg: "waiting for NASInstance creating > available: NASInstance pvc-TESTPVCUID status: unknown",
		},
		"ip from pvc annotation":{
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 10 * util.Gb, 0, "10.100.0.0/28", false),
			res: initCreateVolumeResponse("testregion/pvc-TESTPVCUID", 100 * util.Gb, "192.168.100.0", ""),
			post: []nas.NASInstance{initNASInstance("pvc-TESTPVCUID", "192.168.100.0", 100)},
		},
		"take neighbor ip":{
			obj: []runtime.Object{
				newPVC("testpvc", "100Gi", "TESTPVCUID"),
				newPVC("testpvc-pre", "100Gi", "TESTPVCUID-pre"),
			},
			pre: []nas.NASInstance{initNASInstance("pvc-TESTPVCUID-pre", "192.168.100.0", 100)},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 100 * util.Gb, 0, "192.168.100.0/28", false),
			res: initCreateVolumeResponse("testregion/pvc-TESTPVCUID", 100 * util.Gb, "192.168.100.1", ""),
			post: []nas.NASInstance{
				initNASInstance("pvc-TESTPVCUID-pre", "192.168.100.0", 100),
				initNASInstance("pvc-TESTPVCUID", "192.168.100.1", 100),
			},
		},
		"valid shared volume":{
			obj: []runtime.Object{
				newPVC("testpvc", "100Gi", "TESTPVCUID"),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 100 * util.Gb, 0, "192.168.100.0/28", true),
			res: initCreateVolumeResponse("testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID", 100 * util.Gb, "192.168.100.0", "pvc-TESTPVCUID"),
			post: []nas.NASInstance{initNASInstance("cluster-TESTCLUSTERUID-shd001-" + sharedSuffix, "192.168.100.0", 500)},
		},
		"room shared volume":{
			obj: []runtime.Object{
				newPVC("testpvc-pre", "100Gi", "TESTPVCUID-pre"),
				newPV("pvc-TESTPVCUID-pre", "testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID-pre", "100Gi"),
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			pre: []nas.NASInstance{initNASInstance("cluster-TESTCLUSTERUID-shd001-" + sharedSuffix, "192.168.100.0", 500)},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 10 * util.Gb, 0, "192.168.100.0/28", true),
			res: initCreateVolumeResponse("testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID", 10 * util.Gb, "192.168.100.0", "pvc-TESTPVCUID"),
			post: []nas.NASInstance{initNASInstance("cluster-TESTCLUSTERUID-shd001-" + sharedSuffix, "192.168.100.0", 500)},
		},
		"making source path job failed":{
			obj: []runtime.Object{
				newPVC("testpvc", "100Gi", "TESTPVCUID"),
				newPod("jobpod", "default", "cluster-TESTCLUSTERUID-shd001-" + sharedSuffix),
			},
			req: initCreateVolumeResquest("pvc-TESTPVCUID", 100 * util.Gb, 0, "192.168.100.0/28", true),
			job_failed: true,
			errmsg: "error making source path for shared NASInstance:fake logs",
		},
	}

	// additional params for tests
	cases["unsupported access mode"].req.VolumeCapabilities[0].AccessMode = &csi.VolumeCapability_AccessMode{
		Mode: csi.VolumeCapability_AccessMode_UNKNOWN,
	}
	cases["invalid parameter"].req.Parameters["unknownparam"] = "unknownparamvalue"
	cases["ip from pvc annotation"].obj[0].(*corev1.PersistentVolumeClaim).ObjectMeta.Annotations = map[string]string{
		"testDriverName/reservedIPv4Cidr": "192.168.100.0",
	}
	cases["invalid shared capacity value"].req.Parameters["capacityParInstanceGiB"] = "500Gi"

	flag.Set("logtostderr", "true")
	flag.Lookup("v").Value.Set("4")
	flag.Parse()

	for name, c := range(cases) {
		t.Logf("====== Test case [%s] :", name)
		ctl, _, cloud := initTestController(t, c.obj, c.job_failed)
		cloud.NasInstances = c.pre
		if c.err_on_create {
			cloud.StatusOnCreate = &statusUnknown
		}
		if c.err_after_wait {
			cloud.StatusAfterWait = &statusUnknown
		}
		// test the case
		res, err := ctl.CreateVolume(context.TODO(), c.req)
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
			} else {
				if !reflect.DeepEqual(res, c.res) {
					t.Errorf("response not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.res, res)
				}
				if !reflect.DeepEqual(cloud.NasInstances, c.post) {
					t.Errorf("instance not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.post, cloud.NasInstances)
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

func TestDeleteVolume(t *testing.T) {

	rand.Seed(1)
	sharedSuffix := rand.String(5)

	cases := map[string]struct {
		obj []runtime.Object
		pre []nas.NASInstance
		req *csi.DeleteVolumeRequest
		post []nas.NASInstance
		errmsg string
	}{
		"delete volume":{
			pre: []nas.NASInstance{initNASInstance("pvc-TESTPVCUID", "192.168.100.0", 100)},
			req: &csi.DeleteVolumeRequest{VolumeId: "testregion/pvc-TESTPVCUID"},
			post: []nas.NASInstance{},
		},
		"delete shared volume":{
			obj: []runtime.Object{
				newPV("pvc-TESTPVCUID", "testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID", "100Gi"),
				newPV("pvc-TESTPVCUID2", "testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID2", "100Gi"),
			},
			pre: []nas.NASInstance{initNASInstance("cluster-TESTCLUSTERUID-shd001-" + sharedSuffix, "192.168.100.0", 500)},
			req: &csi.DeleteVolumeRequest{VolumeId: "testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID"},
			post: []nas.NASInstance{initNASInstance("cluster-TESTCLUSTERUID-shd001-" + sharedSuffix, "192.168.100.0", 500)},
		},
		"delete shared nas":{
			obj: []runtime.Object{
				newPV("pvc-TESTPVCUID", "testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID", "100Gi"),
			},
			pre: []nas.NASInstance{initNASInstance("cluster-TESTCLUSTERUID-shd001-" + sharedSuffix, "192.168.100.0", 500)},
			req: &csi.DeleteVolumeRequest{VolumeId: "testregion/cluster-TESTCLUSTERUID-shd001-" + sharedSuffix + "/pvc-TESTPVCUID"},
			post: []nas.NASInstance{},
		},
	}

	flag.Set("logtostderr", "true")
	flag.Lookup("v").Value.Set("4")
	flag.Parse()

	for name, c := range(cases) {
		t.Logf("====== Test case [%s] :", name)
		ctl, _, cloud := initTestController(t, c.obj, false)
		cloud.NasInstances = c.pre
		// test the case
		_, err := ctl.DeleteVolume(context.TODO(), c.req)
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
			} else if !reflect.DeepEqual(cloud.NasInstances, c.post) {
				t.Errorf("instance not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.post, cloud.NasInstances)
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

func TestValidateVolumeCapabilities(t *testing.T) {

	cases := map[string]struct {
		obj []runtime.Object
		pre []nas.NASInstance
		req *csi.ValidateVolumeCapabilitiesRequest
		errmsg string
	}{
		"valid volume":{
			pre: []nas.NASInstance{initNASInstance("pvc-TESTPVCUID", "192.168.100.0", 100)},
			req: &csi.ValidateVolumeCapabilitiesRequest{VolumeId: "testregion/pvc-TESTPVCUID", VolumeCapabilities: initVolumeCapabilities()},
		},
		"volume not found":{
			req: &csi.ValidateVolumeCapabilitiesRequest{VolumeId: "testregion/pvc-TESTPVCUID", VolumeCapabilities: initVolumeCapabilities()},
			errmsg: "NotFound",
		},
		"unsupported access mode":{
			pre: []nas.NASInstance{initNASInstance("pvc-TESTPVCUID", "192.168.100.0", 100)},
			req: &csi.ValidateVolumeCapabilitiesRequest{VolumeId: "testregion/pvc-TESTPVCUID", VolumeCapabilities: initVolumeCapabilities()},
			errmsg: "driver does not support access mode",
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
		ctl, _, cloud := initTestController(t, c.obj, false)
		cloud.NasInstances = c.pre
		// test the case
		_, err := ctl.ValidateVolumeCapabilities(context.TODO(), c.req)
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

// test utils

func initNASInstance(name, ip string, storage int64) nas.NASInstance {
	var preZone = "east-11"
	var preNetwork = "default"
	var preProtocol = "nfs"
	var preInstanceType int64 = 0
	preStorage := fmt.Sprintf("%d", storage)
	return nas.NASInstance{
		AllocatedStorage: &preStorage,
		AvailabilityZone: &preZone,
		NASInstanceIdentifier: &name,
		NASInstanceType: &preInstanceType,
		NASSecurityGroups: []nas.NASSecurityGroup{
			nas.NASSecurityGroup{NASSecurityGroupName: &testSecurityGroupName},
		},
		Endpoint: &nas.Endpoint{PrivateAddress: &ip},
		NetworkId: &preNetwork,
		Protocol: &preProtocol,
		NASInstanceStatus: &statusAvailable,
		NoRootSquash: &valueTrue,
	}
}

func initCreateVolumeResquest(name string, capReq, capLim int64, cidr string, shared bool) *csi.CreateVolumeRequest {
	req := &csi.CreateVolumeRequest{
		Name: name,
		VolumeCapabilities: initVolumeCapabilities(),
		CapacityRange: &csi.CapacityRange{RequiredBytes: capReq, LimitBytes: capLim},
		Parameters: map[string]string{},
	}
	if cidr != "" {
		req.Parameters["reservedIpv4Cidr"] = cidr
	}
	if shared {
		req.Parameters["shared"] = "true"
		req.Parameters["capacityParInstanceGiB"] = "500"
	}
	return req
}

func initVolumeCapabilities() []*csi.VolumeCapability {
	return []*csi.VolumeCapability{
		{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
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

func createVolumeAndPV(ctl csi.ControllerServer, req *csi.CreateVolumeRequest, kubeClient kubernetes.Interface) error {
	res, err := ctl.CreateVolume(context.TODO(), req)
	if err != nil {
		return err
	}
	_, err = kubeClient.CoreV1().PersistentVolumes().Create(
		context.TODO(),
		newPV(req.Name, res.Volume.VolumeId, "100Gi"),
		metav1.CreateOptions{},
	)
	if err != nil {
		return err
	}
	return nil
}

func initTestController(
	t *testing.T,
	objects []runtime.Object,
	job_failed bool) (csi.ControllerServer, kubernetes.Interface, *FakeCloud) {

	// test cloud
	cloud := newFakeCloud()
	// test k8s
	kubeobjects := []runtime.Object{}
	kubeobjects = append(kubeobjects, newNamespace("kube-system", "TESTCLUSTERUID"))
	kubeobjects = append(kubeobjects, objects...)
	kubeClient := k8sfake.NewSimpleClientset(kubeobjects...)
	// all jobs are created with status Complete
	jobCondition :=  batchv1.JobComplete
	if job_failed {
		jobCondition = batchv1.JobFailed
	}
	kubeClient.Fake.PrependReactor("create", "jobs", func(action core.Action) (bool, runtime.Object, error) {
		obj := action.(core.CreateAction).GetObject()
		job, _ := obj.(*batchv1.Job)
		job.Status.Conditions = []batchv1.JobCondition{
			batchv1.JobCondition{Type: jobCondition},
		}
		return false, job, nil
	})
	// init random
	rand.Seed(1)

	driver := initTestDriver(t, cloud, kubeClient, true, false)
	return driver.cs, kubeClient, cloud
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

func newPod(name, namespace, owner string) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				metav1.OwnerReference{Name: owner},
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
	Actions []string
	waitCnt int
	StatusOnCreate *string
	StatusAfterWait *string
}

// Fake Cloud

func newFakeCloud() *FakeCloud {
	return &FakeCloud{
		StatusOnCreate: &statusCreating,
		StatusAfterWait: &statusAvailable,
	}
}

var (
	statusAvailable = "available"
	statusCreating = "creating"
	statusModifying = "modifying"
	statusAuthorizing = "authorizing"
	statusAuthorized = "authorized"
	statusRevoking = "revoking"
	statusUnknown = "unknown"
	valueTrue = "true"
	valueFalse = "false"
)

func (c *FakeCloud) GetNasInstance(ctx context.Context, name string) (*nas.NASInstance, error) {
	c.Actions = append(c.Actions, "GetNasInstance/" + name)
	for i, n := range(c.NasInstances) {
		if *n.NASInstanceIdentifier == name {
			c.waitCnt--
			if c.waitCnt <= 0 {
				c.NasInstances[i].NASInstanceStatus = c.StatusAfterWait
				c.waitCnt = 0
			}
			return &c.NasInstances[i], nil
		}
	}
	return nil, awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}

func (c *FakeCloud) ListNasInstances(ctx context.Context) ([]nas.NASInstance, error) {
	c.Actions = append(c.Actions, "ListNasInstances")
	return c.NasInstances, nil
}

func (c *FakeCloud) CreateNasInstance(ctx context.Context, in *nas.CreateNASInstanceInput) (*nas.NASInstance, error) {
	c.Actions = append(c.Actions, "CreateNasInstance/" + *in.NASInstanceIdentifier)
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
		NASInstanceStatus: c.StatusOnCreate,
		NoRootSquash: &valueFalse,
	}
	c.NasInstances = append(c.NasInstances, n)
	c.waitCnt = 2
	return &n, nil
}

func (c *FakeCloud) ModifyNasInstance(ctx context.Context, name string) (*nas.NASInstance, error) {
	c.Actions = append(c.Actions, "ModifyNasInstance/" + name)
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
	c.Actions = append(c.Actions, "DeleteNasInstance/" + name)
	for i, n := range(c.NasInstances) {
		if *n.NASInstanceIdentifier == name {
			if len(c.NasInstances) > 1 {
				c.NasInstances[i] = c.NasInstances[len(c.NasInstances)-1]
			}
			c.NasInstances = c.NasInstances[:len(c.NasInstances)-1]
			return nil
		}
	}
	return awserr.New("TestAwsErrorNotFound", "", fmt.Errorf("NASInstance %s not found", name))
}

func (c *FakeCloud) GenerateVolumeIdFromNasInstance(obj *nas.NASInstance) string {
	c.Actions = append(c.Actions, "GenerateVolumeIdFromNasInstance/" + *obj.NASInstanceIdentifier)
	return "testregion/" + *obj.NASInstanceIdentifier
}

func (c *FakeCloud) GetNasInstanceFromVolumeId(ctx context.Context, id string) (*nas.NASInstance, error) {
	c.Actions = append(c.Actions, "GetNasInstanceFromVolumeId/" + id)
	tokens := strings.Split(id, "/")
	if len(tokens) != 2 {
		return nil, fmt.Errorf("volume id %q unexpected format: got %v tokens", id, len(tokens))
	}

	return c.GetNasInstance(ctx, tokens[1])
}
