package driver

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"github.com/golang/protobuf/ptypes"
	"golang.org/x/net/context"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// ResticSnapshot holds informations of a restic snapshot
type ResticSnapshot struct {
	ShortID  string   `json:"short_id"`
	ID       string   `json:"id"`
	Time     string   `json:"time"`
	Paths    []string `json:"paths"`
	Hostname string   `json:"hostname"`
	Username string   `json:"username"`
	Tree     string   `json:"tree"`
	Parent   string   `json:"parent"`
}

// GetSourceVolumeID returns the path name of the snapshot
func (snap *ResticSnapshot) GetSourceVolumeID() string {
	if len(snap.Paths) == 0 {
		return ""
	}
	_, file := filepath.Split(snap.Paths[0])
	return file
}

// ResticBackupSummary holds informations of a restic snapshot summary
type ResticBackupSummary struct {
	FilesNew            int64   `json:"files_new"`
	FilesChanges        int64   `json:"files_changed"`
	FilesUnmodified     int64   `json:"files_unmodified"`
	DirsNew             int64   `json:"dirs_new"`
	DirsChanges         int64   `json:"dirs_changed"`
	DirsModified        int64   `json:"dirs_unmodified"`
	DataBlobs           int64   `json:"data_blobs"`
	TreeBlobs           int64   `json:"tree_blobs"`
	DataAdded           int64   `json:"data_added"`
	TotalFilesProcessed int64   `json:"total_files_processed"`
	TotalBytesProcessed int64   `json:"total_bytes_processed"`
	TotalDuration       float64 `json:"total_duration"`
	SnapshotID          string  `json:"snapshot_id"`
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
	volumeID := snapshot.GetSourceVolumeID()
	if volumeID == "" {
		return nil, fmt.Errorf("Error getting SourceVolumeID")
	}
	snap := &csi.Snapshot{
		SnapshotId:     snapshot.ShortID,
		SourceVolumeId: volumeID,
		CreationTime:   tsp,
		ReadyToUse:     true,
	}
	if sizeBytes != 0 {
		snap.SizeBytes = sizeBytes
	}
	return snap, nil
}

type restic struct {
	pw         string
	accesskey  string
	secretkey  string
	repository string
	image      string
}

func newRestic(secrets map[string]string) (*restic, error) {

	if secrets["accesskey"] == "" {
		accesskey := os.Getenv("AWS_ACCESS_KEY_ID")
		if accesskey == "" {
			return nil, fmt.Errorf("cannot get accesskey from env or secrets")
		}
		secrets["accesskey"] = accesskey
	}
	if secrets["secretkey"] == "" {
		secretkey := os.Getenv("AWS_SECRET_ACCESS_KEY")
		if secretkey == "" {
			return nil, fmt.Errorf("cannot get secretkey from env or secrets")
		}
		secrets["secretkey"] = secretkey
	}
	if secrets["resticRepository"] == "" {
		return nil, fmt.Errorf("'resticRepository' not found in restic secrets")
	}
	if secrets["resticPassword"] == "" {
		return nil, fmt.Errorf("'resticPassword' not found in restic secrets")
	}

	return &restic{
		pw:         secrets["resticPassword"],
		accesskey:  secrets["accesskey"],
		secretkey:  secrets["secretkey"],
		repository: secrets["resticRepository"],
		image:      "restic/restic:0.12.0",
	}, nil
}

func retryNotifyRestic(err error, wait time.Duration) {
	glog.Infof("%s : will be checked again in %.2f seconds", err.Error(), wait.Seconds())
}

var (
	testResticPodLog = map[string]string{}
)

// execute restic Job with backing off
func doResticJob(ctx context.Context, job *batchv1.Job,
	secret *corev1.Secret, kubeClient kubernetes.Interface, initInterval int) (string, error) {

	name := job.GetName()
	namespace := job.GetNamespace()

	// Create Secret
	_, err := kubeClient.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return "", fmt.Errorf("Creating restic secret error - %s", err.Error())
	}
	defer func() {
		err := kubeClient.CoreV1().Secrets(namespace).Delete(ctx, secret.GetName(), metav1.DeleteOptions{})
		if err != nil {
			glog.Warningf("error deleting secret %s in doResticJob : %s", secret.GetName(), err.Error())
		}
	}()

	// Create job
	var dp metav1.DeletionPropagation = metav1.DeletePropagationForeground
	var gps int64 = 0
	_, err = kubeClient.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		if !errors.IsAlreadyExists(err) {
			return "", fmt.Errorf("Creating restic job error - %s", err.Error())
		}
		err = kubeClient.BatchV1().Jobs(namespace).Delete(
			ctx, name, metav1.DeleteOptions{PropagationPolicy: &dp, GracePeriodSeconds: &gps})
		if err != nil {
			glog.Warningf("error deleting job %s in doResticJob : %s", name, err.Error())
		}
		_, err = kubeClient.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
		if err != nil {
			return "", fmt.Errorf("Re-creating restic job error - %s", err.Error())
		}
	}
	defer func() {
		err := kubeClient.BatchV1().Jobs(namespace).Delete(
			ctx, name, metav1.DeleteOptions{PropagationPolicy: &dp, GracePeriodSeconds: &gps})
		if err != nil {
			glog.Warningf("error deleting job %s in doResticJob : %s", name, err.Error())
		}
	}()

	// wait for job completed with backoff retry
	var errBuf error
	errBuf = nil
	b := backoff.NewExponentialBackOff()
	b.MaxElapsedTime = time.Duration(30) * time.Minute
	b.RandomizationFactor = 0.2
	b.Multiplier = 2.0
	b.InitialInterval = time.Duration(initInterval) * time.Second
	chkJobCompleted := func() error {
		chkJob, err := kubeClient.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return backoff.Permanent(err)
		}
		if len(chkJob.Status.Conditions) > 0 {
			if chkJob.Status.Conditions[0].Type == "Failed" {
				return backoff.Permanent(fmt.Errorf("Job %s failed", name))
			}
			return nil
		}
		return fmt.Errorf("Job %s is running", name)
	}
	err = backoff.RetryNotify(chkJobCompleted, b, retryNotifyRestic)
	if err != nil {
		errBuf = fmt.Errorf("Error doing restic job - %s", err.Error())
	}

	// Get logs
	podList, err := kubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("Listing job pods error - %s", err.Error())
	}
	for _, pod := range podList.Items {
		refs := pod.ObjectMeta.GetOwnerReferences()
		if len(refs) > 0 && refs[0].Name == name {
			req := kubeClient.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
			podLogs, err := req.Stream(ctx)
			if err != nil {
				return "", fmt.Errorf("Logs request error - %s", err.Error())
			}
			reader := bufio.NewReader(podLogs)
			defer func() {
				err := podLogs.Close()
				if err != nil {
					glog.Warningf("error closing pod logs in doResticJob : %s", err.Error())
				}
			}()
			// return a line which contains 'summary' or a last one
			out := ""
			for {
				line, err := reader.ReadString('\n')
				if line != "" {
					out = line
					// for test
					if strings.Contains(out, "fake logs") {
						out = testResticPodLog[name]
					}
				}
				if err != nil || strings.Contains(out, "summary") || strings.Contains(out, "Fatal:") {
					break
				}
			}
			if errBuf != nil {
				return "", fmt.Errorf("%s : %s", out, errBuf.Error())
			}
			return out, nil
		}
	}
	return "", fmt.Errorf("Cannot find pod for job %s", name)
}

// restic snapshots
func (r *restic) resticJobListSnapshots() (*batchv1.Job, *corev1.Secret) {
	job := r.resticJob("restic-job-list-snapshots", "default")
	job.Spec.Template.Spec.Containers[0].Args = append(
		job.Spec.Template.Spec.Containers[0].Args,
		[]string{"snapshots", "--json"}...,
	)
	return job, r.resticSecret("default")
}

// restic backup
func (r *restic) resticJobBackup(volumeID, pvc, namespace, clusterUID string) (*batchv1.Job, *corev1.Secret) {
	job := r.resticJob("restic-job-backup-"+volumeID, namespace)
	job.Spec.Template.Spec.Containers[0].Args = append(
		job.Spec.Template.Spec.Containers[0].Args,
		[]string{"backup", "--json", "--tag", clusterUID, "/" + volumeID}...,
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
		Name:      "pvc",
		MountPath: "/" + volumeID,
		ReadOnly:  true,
	}
	job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
		job.Spec.Template.Spec.Containers[0].VolumeMounts,
		volumeMount,
	)
	job.Spec.Template.Spec.Hostname = "restic-backup-job"
	return job, r.resticSecret(namespace)
}

// restic delete
func (r *restic) resticJobDelete(snapshotID string) (*batchv1.Job, *corev1.Secret) {
	job := r.resticJob("restic-job-delete-"+snapshotID, "default")
	job.Spec.Template.Spec.Containers[0].Args = append(
		job.Spec.Template.Spec.Containers[0].Args,
		[]string{"forget", "--prune", snapshotID}...,
	)
	return job, r.resticSecret("default")
}

// restic init
func (r *restic) resticJobInit() (*batchv1.Job, *corev1.Secret) {
	job := r.resticJob("restic-job-init", "default")
	job.Spec.Template.Spec.Containers[0].Args = append(
		job.Spec.Template.Spec.Containers[0].Args,
		[]string{"init"}...,
	)
	return job, r.resticSecret("default")
}

func parseRepositry(s string) (string, string, string, error) {
	switch {
	case strings.HasPrefix(s, "s3:https//"):
		s = s[10:]
	case strings.HasPrefix(s, "s3:http//"):
		s = s[9:]
	case strings.HasPrefix(s, "s3://"):
		s = s[5:]
	case strings.HasPrefix(s, "s3:"):
		s = s[3:]
	default:
		return "", "", "", fmt.Errorf("repository invalid format")
	}
	path := strings.SplitN(s, "/", 3)
	if len(path) < 2 {
		return "", "", "", fmt.Errorf("cannot parse endpoint and bucket from repository")
	}
	prefix := ""
	if len(path) > 2 {
		prefix = path[2]
	}
	return path[0], path[1], prefix, nil
}

func (r *restic) checkBucket() error {

	// get endpoint, bucket and region
	ep, bucket, _, err := parseRepositry(r.repository)
	if err != nil {
		return err
	}
	// Set default location to avoid sending LocationConstraint option
	// which causes some region problems in creating Nifcloud Object Storage bucket
	region := "us-east-1"
	// session
	creds := credentials.NewStaticCredentials(r.accesskey, r.secretkey, "")
	sess, err := session.NewSession(&aws.Config{
		Credentials: creds,
		Region:      aws.String(region),
		Endpoint:    &ep,
	})
	if err != nil {
		return err
	}
	svc := s3.New(sess)
	// list buckets
	result, err := svc.ListBuckets(nil)
	if err != nil {
		return err
	}
	for _, bu := range result.Buckets {
		if aws.StringValue(bu.Name) == bucket {
			glog.Infof("bucket %s found", bucket)
			return nil
		}
	}
	// Create bucket
	glog.Infof("creating bucket %s in location %s", bucket, region)
	req, _ := svc.CreateBucketRequest(&s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	req.HTTPRequest.Header.Set("Accept-Encoding", "identity")
	err = req.Send()
	if err != nil {
		return err
	}
	// wait for propagation of new bucket
	glog.Infof("waiting for bucket %s available", bucket)
	err = svc.WaitUntilBucketExists(&s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return err
	}
	time.Sleep(30 * time.Second)

	return nil
}

// restic restore
func (r *restic) resticJobRestore(snapID, ip, path, snapPath string) (*batchv1.Job, *corev1.Secret) {
	// Job
	job := r.resticJob("restic-job-restore-"+snapID, "default")
	job.Spec.Template.Spec.Containers[0].Args = append(
		job.Spec.Template.Spec.Containers[0].Args,
		[]string{"restore", "-t", "/mnt", snapID}...,
	)
	volume := corev1.Volume{
		Name: "restore-target",
		VolumeSource: corev1.VolumeSource{
			NFS: &corev1.NFSVolumeSource{
				Server: ip,
				Path:   path,
			},
		},
	}
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, volume)
	volumeMount := corev1.VolumeMount{
		Name:      "restore-target",
		MountPath: filepath.Join("/mnt", snapPath),
	}
	job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
		job.Spec.Template.Spec.Containers[0].VolumeMounts,
		volumeMount,
	)

	return job, r.resticSecret("default")
}

const resticSecretName = "restic-secrets"

func (r *restic) resticSecret(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resticSecretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"resticPassword":   []byte(r.pw),
			"accesskey":        []byte(r.accesskey),
			"secretkey":        []byte(r.secretkey),
			"resticRepository": []byte(r.repository),
		},
	}
}

// restic job pod
func (r *restic) resticJob(name, namespace string) *batchv1.Job {

	backoffLimit := int32(0)
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
							Image: r.image,
							Env: []corev1.EnvVar{
								{
									Name: "AWS_ACCESS_KEY_ID",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: resticSecretName},
											Key:                  "accesskey",
										},
									},
								},
								{
									Name: "AWS_SECRET_ACCESS_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: resticSecretName},
											Key:                  "secretkey",
										},
									},
								},
								{
									Name: "RESTIC_PASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: resticSecretName},
											Key:                  "resticPassword",
										},
									},
								},
								{
									Name: "RESTIC_REPOSITORY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: resticSecretName},
											Key:                  "resticRepository",
										},
									},
								},
							},
						},
					},
					RestartPolicy: "Never",
				},
			},
			BackoffLimit: &backoffLimit,
		},
	}
}
