package driver

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Get PVC name / namespace from volumeID
func getPVCFromVolumeID(ctx context.Context, volumeID string, driver *NifcloudNasDriver) (string, string, error) {
	kubeClient := driver.config.KubeClient
	pvcUIDs := strings.SplitN(volumeID, "-", 2)
	if len(pvcUIDs) < 2 {
		return "", "", fmt.Errorf("Error splitting UID %s", volumeID)
	}
	pvcs, err := kubeClient.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", "", fmt.Errorf("Error getting PVC list : %s", err.Error())
	}
	for _, pvc := range pvcs.Items {
		if string(pvc.ObjectMeta.GetUID()) == pvcUIDs[1] {
			return pvc.ObjectMeta.GetName(), pvc.ObjectMeta.GetNamespace(), nil
		}
	}
	return "", "", fmt.Errorf("Error PVC %s not found", pvcUIDs[1])
}

func (s *controllerServer) CreateSnapshot(
	ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {

	glog.V(4).Infof("CreateSnapshot called with request : Name=%s SourceVolumeID=%s",
		req.GetName(), req.GetSourceVolumeId())

	// Validate arguments
	name := req.GetName()
	if len(name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateSnapshot name must be provided")
	}
	sourceVolumeID := req.GetSourceVolumeId()
	if len(sourceVolumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateSnapshot sourceVolumeId must be provided")
	}
	_, pvID := filepath.Split(sourceVolumeID)
	pvc, namespace, err := getPVCFromVolumeID(ctx, pvID, s.config.driver)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Cannot find pvc bounded to "+name+" : "+err.Error())
	}

	// Backup job
	r, err := newRestic(req.GetSecrets())
	if err != nil {
		return nil, status.Error(codes.Internal, "Error configure restic job : "+err.Error())
	}
	// Get kube-system UID for cluster ID
	clusterUID, err := getNamespaceUID(ctx, "kube-system", s.config.driver)
	if err != nil {
		return nil, status.Error(codes.Internal, "Error getting namespace UID : "+err.Error())
	}
	clusterUID = "cluster-" + clusterUID
	job, secret := r.resticJobBackup(pvID, pvc, namespace, clusterUID)
	output, err := doResticJob(ctx, job, secret, s.config.driver.config.KubeClient, 30)
	if err != nil {
		return nil, status.Error(codes.Internal, "Error running backup job : "+err.Error())
	}

	// Perse backup summary
	jsonBytes := []byte(output)
	summary := new(ResticBackupSummary)
	err = json.Unmarshal(jsonBytes, summary)
	if err != nil {
		return nil, status.Error(codes.Internal, "Error persing restic backup summary : "+err.Error()+" : "+output)
	}

	// List job
	job, secret = r.resticJobListSnapshots()
	output, err = doResticJob(ctx, job, secret, s.config.driver.config.KubeClient, 5)
	if err != nil {
		return nil, status.Error(codes.Internal, "Error running list snapshots job : "+err.Error())
	}

	// Perse snapshot list
	jsonBytes = []byte(output)
	list := []ResticSnapshot{}
	err = json.Unmarshal(jsonBytes, &list)
	if err != nil {
		return nil, status.Error(codes.Internal, "Error persing restic snapshot : "+err.Error()+" : "+output)
	}
	var snapshot *csi.Snapshot = nil
	for i, snap := range list {
		if snap.ShortID == summary.SnapshotID {
			snapshot, err = newCSISnapshot(&list[i], summary.TotalBytesProcessed)
			if err != nil {
				return nil, status.Error(codes.Internal, "Error translating restic snapshot : "+err.Error())
			}
			break
		}
	}
	if snapshot == nil {
		return nil, status.Error(codes.Internal, "Snapshot not found in list")
	}

	glog.V(4).Infof("Snapshot %s successfully created from volume %s", snapshot.SnapshotId, sourceVolumeID)

	return &csi.CreateSnapshotResponse{Snapshot: snapshot}, nil
}

func (s *controllerServer) DeleteSnapshot(
	ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {

	glog.V(4).Infof("DeleteSnapshot called with args : SnapshotId=%s", req.GetSnapshotId())

	snapshotID := req.GetSnapshotId()
	if len(snapshotID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Snapshot ID not provided")
	}

	// Delete job
	r, err := newRestic(req.GetSecrets())
	if err != nil {
		return nil, status.Error(codes.Internal, "Error configure restic job : "+err.Error())
	}
	job, secret := r.resticJobDelete(snapshotID)
	output, err := doResticJob(ctx, job, secret, s.config.driver.config.KubeClient, 10)
	if err != nil {
		return nil, status.Error(codes.Internal, "Error running delete snapshot job : "+err.Error()+" : "+output)
	}

	glog.V(4).Infof("Snapshot %s successfully deleted", snapshotID)

	return &csi.DeleteSnapshotResponse{}, nil
}

func (s *controllerServer) ListSnapshots(
	ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {

	glog.V(4).Infof("ListSnapshots called with args : SnapshotId=%s", req.GetSnapshotId())

	// List job
	r, err := newRestic(req.GetSecrets())
	if err != nil {
		return nil, status.Error(codes.Internal, "Error configure restic job : "+err.Error())
	}
	job, secret := r.resticJobListSnapshots()
	output, err := doResticJob(ctx, job, secret, s.config.driver.config.KubeClient, 5)
	if err != nil {
		return nil, status.Error(codes.Internal, "Error running list snapshots job : "+err.Error()+" : "+output)
	}

	// Perse snapshot list and return
	jsonBytes := []byte(output)
	list := []ResticSnapshot{}
	err = json.Unmarshal(jsonBytes, &list)
	if err != nil {
		return nil, status.Error(codes.Internal, "Error persing restic backup summary : "+err.Error()+" : "+output)
	}

	snapshotID := req.GetSnapshotId()

	// find a snapshot
	if len(snapshotID) != 0 {
		for i, snap := range list {
			if snap.ShortID == snapshotID {
				snapshot, err := newCSISnapshot(&list[i], 0)
				if err != nil {
					return nil, status.Error(codes.Internal, "Error translating restic snapshot : "+err.Error())
				}
				return &csi.ListSnapshotsResponse{
					Entries: []*csi.ListSnapshotsResponse_Entry{&csi.ListSnapshotsResponse_Entry{Snapshot: snapshot}},
				}, nil
			}
		}
		// Not found
		return &csi.ListSnapshotsResponse{}, nil
	}

	// list for source volumeID or all
	volumeID := req.GetSourceVolumeId()
	var entries []*csi.ListSnapshotsResponse_Entry
	for i, snap := range list {
		if len(volumeID) == 0 || volumeID == snap.GetSourceVolumeID() {
			snapshot, err := newCSISnapshot(&list[i], 0)
			if err != nil {
				return nil, status.Error(codes.Internal, "Error translating restic snapshot : "+err.Error())
			}
			entries = append(entries, &csi.ListSnapshotsResponse_Entry{Snapshot: snapshot})
		}
	}
	return &csi.ListSnapshotsResponse{Entries: entries}, nil
}

// Get snapshotclass secrets from snapshotID
func getSecretsFromSnapshotID(
	ctx context.Context, snapshotID string, driver *NifcloudNasDriver) (map[string]string, error) {

	m := make(map[string]string)

	snapClient := driver.config.SnapClient
	kubeClient := driver.config.KubeClient
	contents, err := snapClient.SnapshotV1beta1().VolumeSnapshotContents().List(ctx, metav1.ListOptions{})
	if err != nil {
		return m, fmt.Errorf("Error getting volumesnapshotcontents list : %s", err.Error())
	}
	className := ""
	for _, c := range contents.Items {
		if *c.Status.SnapshotHandle == snapshotID {
			className = *c.Spec.VolumeSnapshotClassName
			break
		}
	}
	if className == "" {
		return m, fmt.Errorf("Error snapshotId %s not found in volumesnapshotcontents", snapshotID)
	}
	class, err := snapClient.SnapshotV1beta1().VolumeSnapshotClasses().Get(ctx, className, metav1.GetOptions{})
	if err != nil {
		return m, fmt.Errorf("Error getting volumesnapshotclass %s : %s", className, err.Error())
	}
	if class.Parameters["csi.storage.k8s.io/snapshotter-secret-name"] == "" {
		return m, fmt.Errorf("Error csi.storage.k8s.io/snapshotter-secret-name not found in parameters")
	}
	if class.Parameters["csi.storage.k8s.io/snapshotter-secret-namespace"] == "" {
		return m, fmt.Errorf("Error csi.storage.k8s.io/snapshotter-secret-namespace not found in parameters")
	}
	secret, err := kubeClient.CoreV1().Secrets(
		class.Parameters["csi.storage.k8s.io/snapshotter-secret-namespace"],
	).Get(
		ctx,
		class.Parameters["csi.storage.k8s.io/snapshotter-secret-name"],
		metav1.GetOptions{},
	)
	if err != nil {
		return m, fmt.Errorf("Error getting secret : %s", err.Error())
	}
	for key, value := range secret.Data {
		m[key] = string(value)
	}
	return m, nil
}

func (s *controllerServer) restoreSnapshot(ctx context.Context, snapshotID string, vol *csi.Volume) error {

	// Get secrets
	secrets, err := getSecretsFromSnapshotID(ctx, snapshotID, s.config.driver)
	if err != nil {
		return fmt.Errorf("Error getting secrets for %s : %s", snapshotID, err.Error())
	}

	r, err := newRestic(secrets)
	if err != nil {
		return fmt.Errorf("Error configure restic job : %s", err.Error())
	}
	// List job
	job, secret := r.resticJobListSnapshots()
	output, err := doResticJob(ctx, job, secret, s.config.driver.config.KubeClient, 5)
	if err != nil {
		return fmt.Errorf("Error running list snapshots job : %s", err.Error())
	}

	// Perse snapshot list and return
	jsonBytes := []byte(output)
	list := []ResticSnapshot{}
	err = json.Unmarshal(jsonBytes, &list)
	if err != nil {
		return fmt.Errorf("Error persing restic snapshot : %s : %s", err.Error(), output)
	}
	var snap *ResticSnapshot = nil
	for i, snp := range list {
		if snp.ShortID == snapshotID {
			snap = &list[i]
			break
		}
	}
	if snap == nil {
		return fmt.Errorf("Snapshot not found in list")
	}

	// Restore job
	job, secret = r.resticJobRestore(
		snapshotID,
		vol.VolumeContext[attrIP],
		filepath.Join("/", vol.VolumeContext[attrSourcePath]),
		snap.GetSourceVolumeID(),
	)
	output, err = doResticJob(ctx, job, secret, s.config.driver.config.KubeClient, 30)
	if err != nil {
		return fmt.Errorf("Error running restore snapshot job : %s : %s", err.Error(), output)
	}

	return nil
}
