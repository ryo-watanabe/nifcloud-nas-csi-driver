package driver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/protobuf/ptypes"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
	snapfake "github.com/kubernetes-csi/external-snapshotter/client/v4/clientset/versioned/fake"
	"github.com/ryo-watanabe/nifcloud-nas-csi-driver/pkg/util"
	"golang.org/x/net/context"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
)

func TestCreateSnapshot(t *testing.T) {

	ts, _ := time.Parse(time.RFC3339Nano, "2021-01-01T12:00:00.00Z")
	tsp, _ := ptypes.TimestampProto(ts)

	testStorageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprintln(w, listAllBucketsResponse)
	}))
	defer testStorageServer.Close()
	testStorageURL, _ := url.Parse(testStorageServer.URL)

	cases := map[string]struct {
		obj       []runtime.Object
		podLogs   map[string]string
		req       *csi.CreateSnapshotRequest
		res       *csi.CreateSnapshotResponse
		failedJob string
		errmsg    string
	}{
		"valid snapshot": {
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
				newPod("jobpod-backup", "default", "restic-job-backup-pvc-TESTPVCUID"),
				newPod("jobpod-list", "default", "restic-job-list-snapshots"),
			},
			podLogs: map[string]string{
				"restic-job-backup-pvc-TESTPVCUID": "{\"snapshot_id\":\"testSnapshotId\"," +
					"\"total_bytes_processed\":131072}",
				"restic-job-list-snapshots": "[{\"short_id\":\"testSnapshotId\"," +
					"\"time\":\"2021-01-01T12:00:00.00Z\",\"paths\":[\"TESTCLUSTERUID/TESTPVCUID\"]}]",
			},
			req: &csi.CreateSnapshotRequest{
				Name:           "testSnapshot",
				SourceVolumeId: "pvc-TESTPVCUID",
				Secrets: map[string]string{
					"accesskey":        "testAccessKey",
					"secretkey":        "testSecretKey",
					"resticRepository": "testResticRepository",
					"resticPassword":   "testResticPassword",
				},
			},
			res: &csi.CreateSnapshotResponse{
				Snapshot: &csi.Snapshot{
					SnapshotId:     "testSnapshotId",
					SourceVolumeId: "TESTPVCUID",
					CreationTime:   tsp,
					ReadyToUse:     true,
					SizeBytes:      131072,
				},
			},
		},
		"snapshot without name": {
			req:    &csi.CreateSnapshotRequest{},
			errmsg: "name must be provided",
		},
		"snapshot without source volume id": {
			req: &csi.CreateSnapshotRequest{
				Name: "testSnapshot",
			},
			errmsg: "sourceVolumeId must be provided",
		},
		"snapshot without target PVC": {
			req: &csi.CreateSnapshotRequest{
				Name:           "testSnapshot",
				SourceVolumeId: "pvc-TESTPVCUID",
			},
			errmsg: "Cannot find pvc bounded to testSnapshot",
		},
		"snapshot without secret": {
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			req: &csi.CreateSnapshotRequest{
				Name:           "testSnapshot",
				SourceVolumeId: "pvc-TESTPVCUID",
			},
			errmsg: "Error configure restic job : secrets must not be nil",
		},
		"snapshot job failed": {
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
				newPod("jobpod-list", "default", "restic-job-list-snapshots"),
			},
			req: &csi.CreateSnapshotRequest{
				Name:           "testSnapshot",
				SourceVolumeId: "pvc-TESTPVCUID",
				Secrets: map[string]string{
					"accesskey":        "testAccessKey",
					"secretkey":        "testSecretKey",
					"resticRepository": "testResticRepository",
					"resticPassword":   "testResticPassword",
				},
			},
			failedJob: "restic-job-list-snapshots",
			errmsg:    "Error doing restic job - Job restic-job-list-snapshots failed",
		},
		"bucket not found": {
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
				newPod("jobpod-list", "default", "restic-job-list-snapshots"),
			},
			podLogs: map[string]string{
				"restic-job-list-snapshots": "specified key does not exist",
			},
			req: &csi.CreateSnapshotRequest{
				Name:           "testSnapshot",
				SourceVolumeId: "pvc-TESTPVCUID",
				Secrets: map[string]string{
					"accesskey":        "testAccessKey",
					"secretkey":        "testSecretKey",
					"resticRepository": "s3:http//" + testStorageURL.Hostname() + ":" + testStorageURL.Port() + "/bucket",
					"resticPassword":   "testResticPassword",
				},
			},
			failedJob: "restic-job-list-snapshots",
			errmsg:    "Checking bucket failed : bucket not found",
		},
		"initialize repository": {
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
				newPod("jobpod-list", "default", "restic-job-list-snapshots"),
				newPod("jobpod-init", "default", "restic-job-init"),
			},
			podLogs: map[string]string{
				"restic-job-list-snapshots": "specified key does not exist",
			},
			req: &csi.CreateSnapshotRequest{
				Name:           "testSnapshot",
				SourceVolumeId: "pvc-TESTPVCUID",
				Secrets: map[string]string{
					"accesskey":        "testAccessKey",
					"secretkey":        "testSecretKey",
					"resticRepository": "s3:http//" + testStorageURL.Hostname() + ":" + testStorageURL.Port() + "/my-first-bucket",
					"resticPassword":   "testResticPassword",
				},
			},
			failedJob: "restic-job-list-snapshots",
			errmsg:    "Cannot find pod for job restic-job-backup-pvc-TESTPVCUID", // will stops here
		},
		"snapshot summary parse failed": {
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
				newPod("jobpod-backup", "default", "restic-job-backup-pvc-TESTPVCUID"),
				newPod("jobpod-list", "default", "restic-job-list-snapshots"),
			},
			podLogs: map[string]string{
				"restic-job-backup-pvc-TESTPVCUID": "fake logs",
				"restic-job-list-snapshots": "[{\"short_id\":\"testSnapshotId\"," +
					"\"time\":\"2021-01-01T12:00:00.00Z\",\"paths\":[\"TESTCLUSTERUID/TESTPVCUID\"]}]",
			},
			req: &csi.CreateSnapshotRequest{
				Name:           "testSnapshot",
				SourceVolumeId: "pvc-TESTPVCUID",
				Secrets: map[string]string{
					"accesskey":        "testAccessKey",
					"secretkey":        "testSecretKey",
					"resticRepository": "testResticRepository",
					"resticPassword":   "testResticPassword",
				},
			},
			errmsg: "Error persing restic backup summary : unexpected end of JSON input",
		},
	}

	flagVSet("4")

	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		ctl := initTestControllerSnapshot(t, c.obj, []runtime.Object{}, c.failedJob)
		// test the case
		testResticPodLog = c.podLogs
		res, err := ctl.CreateSnapshot(context.TODO(), c.req)
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
			} else if !reflect.DeepEqual(res, c.res) {
				t.Errorf("result not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.res, res)
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

func TestDeleteSnapshot(t *testing.T) {

	cases := map[string]struct {
		obj       []runtime.Object
		podLogs   map[string]string
		req       *csi.DeleteSnapshotRequest
		failedJob string
		errmsg    string
	}{
		"successfully deleted snapshot": {
			obj: []runtime.Object{
				newPod("jobpod-delete", "default", "restic-job-delete-testSnapshotId"),
			},
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: "testSnapshotId",
				Secrets: map[string]string{
					"accesskey":        "testAccessKey",
					"secretkey":        "testSecretKey",
					"resticRepository": "testResticRepository",
					"resticPassword":   "testResticPassword",
				},
			},
		},
		"delete snapshot without snapshot id": {
			req:    &csi.DeleteSnapshotRequest{},
			errmsg: "Snapshot ID not provided",
		},
		"delete snapshot without restic repository": {
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: "testSnapshotId",
				Secrets: map[string]string{
					"accesskey": "testAccessKey",
					"secretkey": "testSecretKey",
				},
			},
			errmsg: "Error configure restic job : 'resticRepository' not found in restic secrets",
		},
	}

	flagVSet("4")

	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		ctl := initTestControllerSnapshot(t, c.obj, []runtime.Object{}, c.failedJob)
		// test the case
		testResticPodLog = c.podLogs
		_, err := ctl.DeleteSnapshot(context.TODO(), c.req)
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
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

func TestListSnapshot(t *testing.T) {

	ts, _ := time.Parse(time.RFC3339Nano, "2021-01-01T12:00:00.00Z")
	tsp, _ := ptypes.TimestampProto(ts)
	ts2, _ := time.Parse(time.RFC3339Nano, "2021-01-02T12:00:00.00Z")
	tsp2, _ := ptypes.TimestampProto(ts2)

	cases := map[string]struct {
		obj       []runtime.Object
		podLogs   map[string]string
		req       *csi.ListSnapshotsRequest
		res       *csi.ListSnapshotsResponse
		failedJob string
		errmsg    string
	}{
		"successfully get list": {
			obj: []runtime.Object{
				newPod("jobpod-list", "default", "restic-job-list-snapshots"),
			},
			podLogs: map[string]string{
				"restic-job-list-snapshots": "[" +
					"{\"short_id\":\"testSnapshotId\"," +
					"\"time\":\"2021-01-01T12:00:00.00Z\"," +
					"\"paths\":[\"TESTCLUSTERUID/TESTPVCUID\"]}," +
					"{\"short_id\":\"testSnapshotId2\"," +
					"\"time\":\"2021-01-02T12:00:00.00Z\"," +
					"\"paths\":[\"TESTCLUSTERUID/TESTPVCUID2\"]}" +
					"]",
			},
			req: &csi.ListSnapshotsRequest{
				Secrets: map[string]string{
					"accesskey":        "testAccessKey",
					"secretkey":        "testSecretKey",
					"resticRepository": "testResticRepository",
					"resticPassword":   "testResticPassword",
				},
			},
			res: &csi.ListSnapshotsResponse{
				Entries: []*csi.ListSnapshotsResponse_Entry{
					&csi.ListSnapshotsResponse_Entry{
						Snapshot: &csi.Snapshot{
							SnapshotId:     "testSnapshotId",
							SourceVolumeId: "TESTPVCUID",
							CreationTime:   tsp,
							ReadyToUse:     true,
							SizeBytes:      0,
						},
					},
					&csi.ListSnapshotsResponse_Entry{
						Snapshot: &csi.Snapshot{
							SnapshotId:     "testSnapshotId2",
							SourceVolumeId: "TESTPVCUID2",
							CreationTime:   tsp2,
							ReadyToUse:     true,
							SizeBytes:      0,
						},
					},
				},
			},
		},
		"successfully get a snapshot": {
			obj: []runtime.Object{
				newPod("jobpod-list", "default", "restic-job-list-snapshots"),
			},
			podLogs: map[string]string{
				"restic-job-list-snapshots": "[" +
					"{\"short_id\":\"testSnapshotId\"," +
					"\"time\":\"2021-01-01T12:00:00.00Z\"," +
					"\"paths\":[\"TESTCLUSTERUID/TESTPVCUID\"]}," +
					"{\"short_id\":\"testSnapshotId2\"," +
					"\"time\":\"2021-01-02T12:00:00.00Z\"," +
					"\"paths\":[\"TESTCLUSTERUID/TESTPVCUID2\"]}" +
					"]",
			},
			req: &csi.ListSnapshotsRequest{
				SnapshotId: "testSnapshotId",
				Secrets: map[string]string{
					"accesskey":        "testAccessKey",
					"secretkey":        "testSecretKey",
					"resticRepository": "testResticRepository",
					"resticPassword":   "testResticPassword",
				},
			},
			res: &csi.ListSnapshotsResponse{
				Entries: []*csi.ListSnapshotsResponse_Entry{
					&csi.ListSnapshotsResponse_Entry{
						Snapshot: &csi.Snapshot{
							SnapshotId:     "testSnapshotId",
							SourceVolumeId: "TESTPVCUID",
							CreationTime:   tsp,
							ReadyToUse:     true,
							SizeBytes:      0,
						},
					},
				},
			},
		},
		"list snapshots without restic password": {
			req: &csi.ListSnapshotsRequest{
				SnapshotId: "testSnapshotId",
				Secrets: map[string]string{
					"accesskey":        "testAccessKey",
					"secretkey":        "testSecretKey",
					"resticRepository": "testResticRepository",
				},
			},
			errmsg: "Error configure restic job : 'resticPassword' not found in restic secrets",
		},
	}

	flagVSet("4")

	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		ctl := initTestControllerSnapshot(t, c.obj, []runtime.Object{}, c.failedJob)
		// test the case
		testResticPodLog = c.podLogs
		res, err := ctl.ListSnapshots(context.TODO(), c.req)
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
			} else if !reflect.DeepEqual(res, c.res) {
				t.Errorf("result not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.res, res)
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

var (
	testVolumeSnapshotClass = "testVolumeSnapshotClass"
	testSnapshotID          = "testSnapshotId"
	restoreRequest          = &csi.CreateVolumeRequest{
		Name:               "pvc-TESTPVCUID",
		VolumeCapabilities: initVolumeCapabilities(),
		CapacityRange:      &csi.CapacityRange{RequiredBytes: 10 * util.Gb, LimitBytes: 0},
		Parameters: map[string]string{
			"reservedIpv4Cidr": "192.168.100.0/28",
			"networkId":        "default",
		},
		VolumeContentSource: &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Snapshot{
				Snapshot: &csi.VolumeContentSource_SnapshotSource{
					SnapshotId: "testSnapshotId",
				},
			},
		},
	}
	snapshotContent = &snapv1.VolumeSnapshotContent{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "snapshot.storage.k8s.io/v1",
			Kind:       "VolumeSnapshotContent",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "testSnapshotContent",
		},
		Spec: snapv1.VolumeSnapshotContentSpec{
			VolumeSnapshotClassName: &testVolumeSnapshotClass,
		},
		Status: &snapv1.VolumeSnapshotContentStatus{
			SnapshotHandle: &testSnapshotID,
		},
	}
	snapshotClass = &snapv1.VolumeSnapshotClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "snapshot.storage.k8s.io/v1",
			Kind:       "VolumeSnapshotClass",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "testVolumeSnapshotClass",
		},
		Parameters: map[string]string{
			"csi.storage.k8s.io/snapshotter-secret-name":      "testSnapshotSecretName",
			"csi.storage.k8s.io/snapshotter-secret-namespace": "testSnapshotSecretNamespace",
		},
	}
)

func TestRestoreSnapshot(t *testing.T) {

	//rand.Seed(1)
	//sharedSuffix := rand.String(5)

	cases := map[string]struct {
		obj       []runtime.Object
		sObj      []runtime.Object
		podLogs   map[string]string
		req       *csi.CreateVolumeRequest
		res       *csi.CreateVolumeResponse
		failedJob string
		errmsg    string
	}{
		"successfully volume restored": {
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
				&corev1.Secret{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1",
						Kind:       "Secret",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testSnapshotSecretName",
						Namespace: "testSnapshotSecretNamespace",
					},
					Data: map[string][]byte{
						"accesskey":        []byte("dGVzdEFjY2Vzc0tleQ=="),
						"secretkey":        []byte("dGVzdFNlY3JldEtleQ=="),
						"resticRepository": []byte("cmVzdGljUmVwb3NpdG9yeQ=="),
						"resticPassword":   []byte("cmVzdGljUGFzc3dvcmQ="),
					},
				},
				newPod("jobpod-list", "default", "restic-job-list-snapshots"),
				newPod("jobpod-restore", "default", "restic-job-restore-testSnapshotId"),
			},
			podLogs: map[string]string{
				"restic-job-restore-testSnapshotId": "restore completed",
				"restic-job-list-snapshots": "[{\"short_id\":\"testSnapshotId\"," +
					"\"time\":\"2021-01-01T12:00:00.00Z\"," +
					"\"paths\":[\"TESTCLUSTERUID/TESTPVCUID\"]}]",
			},
			sObj: []runtime.Object{
				snapshotContent,
				snapshotClass,
			},
			req: restoreRequest,
			res: &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					CapacityBytes: 100 * util.Gb,
					VolumeId:      "testregion/pvc-TESTPVCUID",
					VolumeContext: map[string]string{
						attrIP:         "192.168.100.0",
						attrSourcePath: "",
					},
					ContentSource: &csi.VolumeContentSource{
						Type: &csi.VolumeContentSource_Snapshot{
							Snapshot: &csi.VolumeContentSource_SnapshotSource{
								SnapshotId: "testSnapshotId",
							},
						},
					},
				},
			},
		},
		"restore content not found": {
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			req:    restoreRequest,
			errmsg: "Error snapshotId testSnapshotId not found in volumesnapshotcontents",
		},
		"snapshot secret not found": {
			obj: []runtime.Object{
				newPVC("testpvc", "10Gi", "TESTPVCUID"),
			},
			sObj: []runtime.Object{
				snapshotContent,
				snapshotClass,
			},
			req:    restoreRequest,
			errmsg: "Error getting secret : secrets \"testSnapshotSecretName\" not found",
		},
	}

	flagVSet("4")

	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		ctl := initTestControllerSnapshot(t, c.obj, c.sObj, c.failedJob)
		testResticPodLog = c.podLogs
		res, err := ctl.CreateVolume(context.TODO(), c.req)
		if c.errmsg == "" {
			if err != nil {
				t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
			} else if !reflect.DeepEqual(res, c.res) {
				t.Errorf("result not matched in case [%s]\nexpected : %v\nbut got  : %v", name, c.res, res)
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

func initTestControllerSnapshot(
	t *testing.T,
	objects []runtime.Object,
	sObjects []runtime.Object,
	failedJob string) csi.ControllerServer {
	// test cloud
	cloud := newFakeCloud()
	// test k8s
	kubeobjects := []runtime.Object{}
	kubeobjects = append(kubeobjects, newNamespace("kube-system", "TESTCLUSTERUID"))
	kubeobjects = append(kubeobjects, objects...)
	kubeClient := k8sfake.NewSimpleClientset(kubeobjects...)
	// test snapshot client
	snapobjects := []runtime.Object{}
	snapobjects = append(snapobjects, sObjects...)
	snapClient := snapfake.NewSimpleClientset(snapobjects...)
	// jobs are created with status Complete or Failed
	kubeClient.Fake.PrependReactor("create", "jobs", func(action core.Action) (bool, runtime.Object, error) {
		obj := action.(core.CreateAction).GetObject()
		job, _ := obj.(*batchv1.Job)
		if job.GetName() == failedJob || failedJob == "all" {
			job.Status.Conditions = []batchv1.JobCondition{
				batchv1.JobCondition{Type: batchv1.JobFailed},
			}
		} else {
			job.Status.Conditions = []batchv1.JobCondition{
				batchv1.JobCondition{Type: batchv1.JobComplete},
			}
		}
		return false, job, nil
	})

	config := &NifcloudNasDriverConfig{
		Name:    "testDriverName",
		Version: "testDriverVersion",
		NodeID:  "testNodeID",
		//PrivateIfName: *privateIfName,
		RunController: true,
		RunNode:       false,
		//Mounter:       mounter,
		Cloud:       cloud,
		KubeClient:  kubeClient,
		SnapClient:  snapClient,
		InitBackoff: 1,
	}

	driver, err := NewNifcloudNasDriver(config)
	if err != nil {
		t.Fatalf("Failed to initialize Nifcloud Nas CSI Driver: %v", err)
	}

	return driver.cs
}

const listAllBucketsResponse = `<ListAllMyBucketsResult xmlns="http://doc.s3.amazonaws.com/doc/2006-03-01/">
  <Owner>
    <ID>NCSS|ABC12345</ID>
    <DisplayName>ABC12345</DisplayName>
  </Owner>
  <Buckets>
    <Bucket>
     <Name>my-first-bucket</Name>
     <CreationDate>2016-06-29T00:00:00.000Z</CreationDate>
   </Bucket>
   <Bucket>
     <Name>sample-bucket</Name>
     <CreationDate>2011-09-29T01:00:00.000Z</CreationDate>
   </Bucket>
  </Buckets>
</ListAllMyBucketsResult>`
