package driver

import (
	"fmt"
	"bufio"
	"time"
	"path/filepath"
	"strings"

	"github.com/cenkalti/backoff"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/protobuf/ptypes"
	"k8s.io/client-go/kubernetes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

type ResticSnapshot struct {
	ShortId  string `json:"short_id"`
	Id       string `json:"id"`
	Time     string `json:"time"`
	Paths    []string `json:"paths"`
	Hostname string `json:"hostname"`
	Username string `json:"username"`
	Tree     string `json:"tree"`
	Parent   string `json:"parent"`
}

func (snap *ResticSnapshot) GetSourceVolumeId() string {
	_, file := filepath.Split(snap.Paths[0])
	return file
}

type ResticBackupSummary struct {
	FilesNew int64 `json:"files_new"`
	FilesChanges int64 `json:"files_changed"`
	FilesUnmodified int64 `json:"files_unmodified"`
	DirsNew int64 `json:"dirs_new"`
	DirsChanges int64 `json:"dirs_changed"`
	DirsModified int64 `json:"dirs_unmodified"`
	DataBlobs int64 `json:"data_blobs"`
	TreeBlobs int64 `json:"tree_blobs"`
	DataAdded int64 `json:"data_added"`
	TotalFilesProcessed int64 `json:"total_files_processed"`
	TotalBytesProcessed int64 `json:"total_bytes_processed"`
	TotalDuration int64 `json:"total_duration"`
	SnapshotId string `json:"snapshot_id"`
}

func newCSISnapshot(snapshot *ResticSnapshot, sizeBytes int64) (*csi.Snapshot, error) {
	ts, err := time.Parse(time.RFC3339Nano, snapshot.Time)
	if err != nil {
		return nil, fmt.Errorf("Error getting timestamp : %s", err.Error())
	}
	tsp, err := ptypes.TimestampProto(ts)
	if err != nil {
		return nil, fmt.Errorf("Error converting timestamp : %s", err.Error())
	}
	volumeId := snapshot.GetSourceVolumeId()
	if volumeId == "" {
		return nil, fmt.Errorf("Error getting SourceVolumeID")
	}
	snap := &csi.Snapshot{
		SnapshotId:     snapshot.ShortId,
		SourceVolumeId: volumeId,
		CreationTime:   tsp,
		ReadyToUse:     true,
	}
	if sizeBytes != 0 {
		snap.SizeBytes = sizeBytes
	}
	return snap, nil
}

type restic struct {
	bucket string
	pw_secret string
	cloud_cred_secret string
	s3_host string
}

func newRestic() *restic {
	return &restic{
		bucket: "restic-test",
		pw_secret: "restic-password",
		cloud_cred_secret: "jp-east2-cred",
		s3_host: "jp-east-2.storage.api.nifcloud.com",
	}
}

// execute restic Job with backing off
func doResticJob(job *batchv1.Job, kubeClient kubernetes.Interface) (string, error) {

	name := job.GetName()
	namespace := job.GetNamespace()

	// Create job
	_, err := kubeClient.BatchV1().Jobs(namespace).Create(job)
	if err != nil {
		return "", fmt.Errorf("Creating restic job error - %s", err.Error())
	}
	defer kubeClient.BatchV1().Jobs(namespace).Delete(name, &metav1.DeleteOptions{})

	// wait for job completed with backoff retry
	b := backoff.NewExponentialBackOff()
	b.MaxElapsedTime = time.Duration(30) * time.Minute
	b.RandomizationFactor = 0.2
	b.Multiplier = 2.0
	b.InitialInterval = 1 * time.Second
	chkJobCompleted := func() error {
		chkJob, err := kubeClient.BatchV1().Jobs(namespace).Get(name, metav1.GetOptions{})
		if err != nil {
			return backoff.Permanent(err)
		}
		if len(chkJob.Status.Conditions) > 0 {
			if chkJob.Status.Conditions[0].Type == "Failed" {
				return backoff.Permanent(fmt.Errorf("Job %s failed", name))
			} else {
				return nil
			}
		}
		return fmt.Errorf("Job %s is running", name)
	}
	err = backoff.RetryNotify(chkJobCompleted, b, retryNotify)
	if err != nil {
		return "", fmt.Errorf("Error doing restic job - %s", err.Error())
	}

	// Get logs
	podList, err := kubeClient.CoreV1().Pods(namespace).List(metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("Listing job pods error - %s", err.Error())
	}
	for _, pod := range(podList.Items) {
		refs := pod.ObjectMeta.GetOwnerReferences()
		if len(refs) > 0 && refs[0].Name == name {
			req := kubeClient.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
			podLogs, err := req.Stream()
			if err != nil {
				return "", fmt.Errorf("Logs request error - %s", err.Error())
			}
			reader := bufio.NewReader(podLogs)
			defer podLogs.Close()
			// return a line which contains 'summary' or a last one
			out := ""
			for {
				line, err := reader.ReadString('\n')
				out = line
				if err != nil || strings.Contains(out, "summary") {
					break
				}
			}
			return out, nil
			//buf := new(bytes.Buffer)
			//_, err = io.Copy(buf, podLogs)
			//if err != nil {
			//	return "", fmt.Errorf("Logs IO error - %s", err.Error())
			//}
			//return buf.String(), nil
		}
	}
	return "", fmt.Errorf("Cannot find pod for job %s", name)
}

// restic snapshots
func (r *restic) resticJobListSnapshots() *batchv1.Job {
	job := r.resticJob("restic-job-list-snapshots", "default")
	job.Spec.Template.Spec.Containers[0].Args = append(
		job.Spec.Template.Spec.Containers[0].Args,
		[]string{"snapshots", "--json"}...,
	)
	return job
}

// restic backup
func (r *restic) resticJobBackup(volumeId, pvc, namespace string) *batchv1.Job {
	job := r.resticJob("restic-job-backup-" + volumeId, namespace)
	job.Spec.Template.Spec.Containers[0].Args = append(
		job.Spec.Template.Spec.Containers[0].Args,
		[]string{"backup", "--json", "/pv/" + volumeId}...,
	)
	volume := corev1.Volume{
		Name: "pvc",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvc,
			},
		},
	}
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, volume)
	volumeMount := corev1.VolumeMount{
		Name: "pvc",
		MountPath: "/pv/" + volumeId,
		ReadOnly: true,
	}
	job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
		job.Spec.Template.Spec.Containers[0].VolumeMounts,
		volumeMount,
	)
	return job
}

// restic delete
func (r *restic) resticJobDelete(snapshotId string) *batchv1.Job {
	job := r.resticJob("restic-job-delete-" + snapshotId, "default")
	job.Spec.Template.Spec.Containers[0].Args = append(
		job.Spec.Template.Spec.Containers[0].Args,
		[]string{"forget", snapshotId}...,
	)
	return job
}

// restic job pod
func (r *restic) resticJob(name, namespace  string) *batchv1.Job {

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  name,
							Image: "restic/restic",
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "restic-pw",
									MountPath: "/etc/restic",
								},
							},
							Env: []corev1.EnvVar{
								{
									Name: "AWS_SECRET_ACCESS_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: r.cloud_cred_secret,
											},
											Key: "accesskey",
										},
									},
								},
								{
									Name: "AWS_SECRET_SECRET_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: r.cloud_cred_secret,
											},
											Key: "secretkey",
										},
									},
								},
								{
									Name: "RESTIC_PASSWORD_FILE",
									Value: "/etc/restic/password",
								},
								{
									Name: "RESTIC_REPOSITORY",
									Value: "s3:" + r.s3_host + "/" + r.bucket,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "restic-pw",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: r.pw_secret,
								},
							},
						},
					},
					RestartPolicy: "Never",
				},
			},
		},
	}
}
