# nifcloud-nas-csi-driver
NAS CSI driver for k8s clusters running on nifcloud

## Features
- Create nifcloud NASInstances triggered by PVCs creation.
- Shared NASInstance for multiple PVCs with source path provisioning
- Auto configure node private IPs, zone, networkID and recommended CIDR block for NASInstances.
- NAS private IP selected from CIDR block of storageclass parameter or PVC annotation.
- Authorize/Revoke node private IPs onto NASSecurityGroup (Node private IPs kept on CSINode annotation).
- Delete NASInstances on deletion of the PVC

## Options
|param|default| | |
|----|----|----|----|
|endpoint|unix:/tmp/csi.sock|CSI endpoint|Optional|
|nodeid||node name in k8s cluster|Required when node=true|
|region|jp-east-1|nifcloud region|Optional|
|kubeconfig||file path for kubeconfig|Optional|
|controller|false|run controller service|Optional|
|node|false|run node service|Optional|
|configurator|true|auto configure node private IPs, zone, networkID and recommended CIDR block|Optional|
|privateipreg|false|register node private IPs from interfaces|Optional<br><br>Set true when configurator is false and confirm privateifname |
|privateifname|ens192|interface name of private network|Optional|

#### Run controller
````
$ /nifcloud-nas-csi-driver \
--endpoint=unix:/csi/csi.sock
--controller=true
````
#### Run node
````
$ /nifcloud-nas-csi-driver \
--endpoint=unix:/csi/csi.sock
--nodeid=cluster01node01
--node=true
````
To run node in Pods (DaemonSet)
````
:
securityContext:
  privileged: true
args:
  - "--endpoint=unix:/csi/csi.sock"
  - "--nodeid=$(KUBE_NODE_NAME)"
  - "--node=true"
env:
  - name: KUBE_NODE_NAME
    valueFrom:
      fieldRef:
        fieldPath: spec.nodeName
volumeMounts:
  - name: kubelet-dir
    mountPath: /var/lib/kubelet
    mountPropagation: "Bidirectional"
:
````

## Deploy and usage

#### Setup and deploy node
```
$ kubectl apply -f manifests/setup.yaml
$ kubectl apply -f manifests/snapshot/setup-snapshot.yaml
$ kubectl apply -f manifests/CSIDriver.yaml

$ kubectl apply -f manifests/node.yaml
```
#### Credentials

Set access/secret keys in a secret of the account with full access for NAS, NAS Firewall and Storage.
```
apiVersion: v1
kind: Secret
metadata:
  namespace: nifcloud-nas-csi-driver
  name: cloud-credential
data:
  accesskey: [base64 access_key]
  secretkey: [base64 secret_key]
```
#### Deploy controller
```
$ kubectl apply -f manifests/cloud-credential.yaml
$ kubectl apply -f manifests/controller.yaml

$ kubectl get pod -n nifcloud-nas-csi-driver
NAME                            READY   STATUS    RESTARTS   AGE
nifcloud-nas-csi-controller-0   3/3     Running   0          16m
nifcloud-nas-csi-node-4mpgw     2/2     Running   0          14m
nifcloud-nas-csi-node-828tz     2/2     Running   0          14m
nifcloud-nas-csi-node-95ggq     2/2     Running   0          14m
```
#### Create PVC
```
kind: PersistentVolumeClaim
apiVersion: v1
metadata:
  name: test-pvc
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: csi-nifcloud-nas-std
  resources:
    requests:
      storage: 100Gi
```
```
$ kubectl apply -f test-pvc.yaml

$ kubectl describe pvc test-pvc
Name:          test-pvc
Namespace:     default
StorageClass:  csi-nifcloud-nas-std
Status:        Bound
Volume:        pvc-e59e3c6a-f415-42d9-940e-376cc0e98141
Labels:        <none>
Annotations:   kubectl.kubernetes.io/last-applied-configuration:
                 {"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"annotations":{},"name":"test-pvc","namespace":"default"},"spec":{"accessMod...
               pv.kubernetes.io/bind-completed: yes
               pv.kubernetes.io/bound-by-controller: yes
               volume.beta.kubernetes.io/storage-provisioner: nas.csi.storage.nifcloud.com
Finalizers:    [kubernetes.io/pvc-protection]
Capacity:      100Gi
Access Modes:  RWX
VolumeMode:    Filesystem
Mounted By:    <none>
Events:
  Type    Reason                 Age                 From                                                                           Message
  ----    ------                 ----                ----                                                                           -------
  Normal  Provisioning           10m                 nas.csi.storage.nifcloud.com_cluster00w1_e45fb395-aa31-4892-9e2d-ecb11fff31c6  External provisioner is provisioning volume for claim "default/test-pvc"
  Normal  ExternalProvisioning   19s (x42 over 10m)  persistentvolume-controller                                                    waiting for a volume to be created, either by external provisioner "nas.csi.storage.nifcloud.com" or manually created by system administrator
  Normal  ProvisioningSucceeded  11s                 nas.csi.storage.nifcloud.com_cluster00w1_e45fb395-aa31-4892-9e2d-ecb11fff31c6  Successfully provisioned volume pvc-e59e3c6a-f415-42d9-940e-376cc0e98141

$ kubectl get pvc test-pvc
NAME       STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS           AGE
test-pvc   Bound    pvc-e59e3c6a-f415-42d9-940e-376cc0e98141   100Gi      RWX            csi-nifcloud-nas-std   12m
```
## Storage class parameters

### Parameters

* Numbers and booleans must set as strings

|param| |example| |
|----|----|----|----|
|zone|Nifcloud zone|east-11|Required|
|instanceType|Nifcloud NAS InstanceType 0/1|"0"|Required|
|networkId|Private network ID|net-0123abcd<br>net-COMMON_PRIVATE|Required|
|reservedIpv4Cidr|IP range NASInstance can use, ignored when PVC annotation reservedIPv4Cidr is set|192.168.10.64/29|Optional|
|capacityParInstanceGiB|Capacity of a Shared NASInstance|"500"|Required when shared="true"|
|shared|Multi PVs shared a NASInstance with source path provisioning|"true"|Optional|

### Pre-installed Storageclasses
4 storageclasses installed on starting CSI controller

|name|instanceType|shared|
|----|----|----|
|csi-nifcloud-nas-std|"0"|not set ("false")|
|csi-nifcloud-nas-hi|"1"|not set ("false")|
|csi-nifcloud-nas-shrd|"0"|"true"|
|csi-nifcloud-nas-shrdhi|"1"|"true"|

csi-nifcloud-nas-std (example)
```
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: csi-nifcloud-nas-std
provisioner: nas.csi.storage.nifcloud.com
parameters:
  zone: east-11
  instanceType: "0"
  networkId: net-0123abcd
  reservedIpv4Cidr: 192.168.10.64/29
```
### Shared NASInstance with source path provisiong
csi-nifcloud-nas-shrd (example)
```
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: csi-nifcloud-nas-shrd
provisioner: nas.csi.storage.nifcloud.com
parameters:
  zone: east-11
  instanceType: "0"
  networkId: net-0123abcd
  reservedIpv4Cidr: 192.168.10.64/29
  capacityParInstanceGiB: "500"
  shared: "true"
```
- Share a NASInstance for multi PVCs by setting Storageclass parameter shared=true.
- For the first PVC create a NASInstance, and for second and after provision instantly by making source pathes on the shared NASInstance.
- Create another shared NASInstance when requested resource sum of PVCs exceeds its capacity.
- Delete shared NASInstance when all PVCs deleted.

### Setting NAS private IP in PVC annotations

```
kind: PersistentVolumeClaim
apiVersion: v1
metadata:
  name: test-pvc
  annotations:
    nas.csi.storage.nifcloud.com/reservedIPv4Cidr: 192.168.10.64
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: csi-nifcloud-nas-std
  resources:
    requests:
      storage: 100Gi
```
- IP address like "192.168.10.64" can be used same as "192.168.10.64/32"
- If no annotations set, StorageClass parameter reservedIPv4Cidr is used for private IP selection

# Volume Snapshots

## Setup
Install CRDs & controller (external-snapshotter : release-4.1)
```
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-4.1/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-4.1/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-4.1/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml

kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-4.1/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/release-4.1/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml
```

VolumeSnapshotClass
```
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: csi-nifcloud-nas-restic
deletionPolicy: Delete
driver: nas.csi.storage.nifcloud.com
parameters:
  csi.storage.k8s.io/snapshotter-secret-name: restic-creds
  csi.storage.k8s.io/snapshotter-secret-namespace: nifcloud-nas-csi-driver
```
Snapshot Secret
```
apiVersion: v1
kind: Secret
metadata:
  name: restic-creds
  namespace: nifcloud-nas-csi-driver
data:
  resticPassword: c2h0ZnczaHZvMzZxYml0dw==
  resticRepository: czM6anAtZWFzdC0yLnN0b3JhZ2UuYXBpLm5pZmNsb3VkLmNvbS9jc2ktc25hcHNob3QtOTVtaGg=
type: Opaque
```
decoded parameters sample
```
resticReository: s3:jp-east-2.storage.api.nifcloud.com/csi-snapshot-95mhh
resticPassword: shtfw3hvo36qbitw
```
## Taking a snapshot

Create VolumeSnapshot
```
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: data-snap
  namespace: gitlab
spec:
  volumeSnapshotClassName: csi-nifcloud-nas-restic
  source:
    persistentVolumeClaimName: data
```
Then a snapshot of the PVC and a VoluemSnapshotContent created.

VolumeSnapshot (Completed)
```
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
:
  name: data-snap
  namespace: gitlab
:
spec:
  source:
    persistentVolumeClaimName: data
  volumeSnapshotClassName: csi-nifcloud-nas-restic
status:
  boundVolumeSnapshotContentName: snapcontent-07616eb3-181d-4c53-a967-8477508d5151
  creationTime: "2021-07-21T03:13:28Z"
  readyToUse: true
  restoreSize: "174682946"
```
VolumeSnapshotContent
```
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotContent
metadata:
  name: snapcontent-07616eb3-181d-4c53-a967-8477508d5151
:
spec:
  deletionPolicy: Delete
  driver: nas.csi.storage.nifcloud.com
  source:
    volumeHandle: jp-east-1/cluster-943e1849-1bc9-4685-8c94-a7b49fcb87f2-shd0001-c4xbr/pvc-66abc38f-9a12-4522-8df6-6155b4a248c4
  volumeSnapshotClassName: csi-nifcloud-nas-restic
  volumeSnapshotRef:
    apiVersion: snapshot.storage.k8s.io/v1
    kind: VolumeSnapshot
    name: data-snap
    namespace: gitlab
:
status:
  creationTime: 1626837208632037506
  readyToUse: true
  restoreSize: 174682946
  snapshotHandle: e752c598
```
* snapshotHandle(=e752c598) is a snapshotID in the restic repository

## Restoreing form a snapshot

Create PVC with a dataSource
```
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: data-restore
  namespace: gitlab
spec:
  storageClassName: csi-nifcloud-nas-shrd
  dataSource:
    name: data-snap
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 20Gi
```
Then restored PVC from the snapshot created
```
# kubectl get pvc -n gitlab
NAME           STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS            AGE
config         Bound    pvc-0ffa86c9-0fe1-41df-9df5-89d460d0cab8   1Gi        RWX            csi-nifcloud-nas-shrd   198d
data           Bound    pvc-66abc38f-9a12-4522-8df6-6155b4a248c4   20Gi       RWX            csi-nifcloud-nas-shrd   198d
data-restore   Bound    pvc-038f14d2-d4f9-4869-bb64-e0d139673ea4   20Gi       RWX            csi-nifcloud-nas-shrd   2m40s
```