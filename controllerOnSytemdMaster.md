# Binaries, configs
Prepare binaries of driver and csi-provisioner(sidecar) from https://github.com/kubernetes-csi/external-provisioner
```
chmod +x csi-provisioner nifcloud-nas-csi-driver
mv csi-provisioner nifcloud-nas-csi-driver /usr/local/bin/
mkdir /etc/kubernetes/csi
```
# Unit files
(TODO) Prepare a kubeconfig for csi, currently using admin.kubeconfig

/etc/systemd/system/csi-provisioner.service
```
[Unit]
Description=Kubernetes CSI external provisioner service
Documentation=https://github.com/kubernetes/kubernetes

[Service]
ExecStart=/usr/local/bin/csi-provisioner \
  --v=4 \
  --timeout=30m \
  --kubeconfig=/root/admin.kubeconfig \
  --csi-address=/etc/kubernetes/csi/csi.sock
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```
/etc/systemd/system/csi-controller.service
```
[Unit]
Description=Kubernetes CSI controller service
Documentation=https://github.com/kubernetes/kubernetes

[Service]
Environment=AWS_ACCESS_KEY_ID=[ACCESS_KEY]
Environment=AWS_SECRET_ACCESS_KEY=[SECRET_KEY]
ExecStart=/usr/local/bin/nfcl-nas-csi-driver \
  --v=4 \
  --endpoint=unix:/etc/kubernetes/csi/csi.sock \
  --controller=true \
  --kubeconfig=/root/admin.kubeconfig \
  --nodeid=master
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```
# Start sidecar and controller
```
systemctl enable csi-provisioner csi-controller
systemctl daemon-reload
systemctl start csi-provisioner csi-controller
```
status
```
# systemctl status csi-provisioner.service
● csi-provisioner.service - Kubernetes CSI external provisioner service
     Loaded: loaded (/etc/systemd/system/csi-provisioner.service; enabled; vendor preset: enabled)
     Active: active (running) since Tue 2021-03-16 17:01:08 JST; 4s ago
       Docs: https://github.com/kubernetes/kubernetes
   Main PID: 3050375 (csi-provisioner)
      Tasks: 4 (limit: 4587)
     Memory: 10.5M
     CGroup: /system.slice/csi-provisioner.service
             mq3050375 /usr/local/bin/csi-provisioner --v=4 --timeout=30m --kubeconfig=/root/admin.kubeconfig --csi-address=/etc/kubernetes/csi/csi.sock

Mar 16 17:01:09 rootless01m1 csi-provisioner[3050375]: I0316 17:01:09.318419 3050375 shared_informer.go:270] caches populated
Mar 16 17:01:09 rootless01m1 csi-provisioner[3050375]: I0316 17:01:09.318463 3050375 shared_informer.go:270] caches populated
Mar 16 17:01:09 rootless01m1 csi-provisioner[3050375]: I0316 17:01:09.318475 3050375 controller.go:838] Starting provisioner controller nas.csi.storage.nifcloud.com_ro>
Mar 16 17:01:09 rootless01m1 csi-provisioner[3050375]: I0316 17:01:09.318517 3050375 volume_store.go:97] Starting save volume queue
```
```
# systemctl status csi-controller.service
● csi-controller.service - Kubernetes CSI controller service
     Loaded: loaded (/etc/systemd/system/csi-controller.service; enabled; vendor preset: enabled)
     Active: active (running) since Tue 2021-03-16 17:01:46 JST; 3s ago
       Docs: https://github.com/kubernetes/kubernetes
   Main PID: 3050843 (nfcl-nas-csi-dr)
      Tasks: 3 (limit: 4587)
     Memory: 12.2M
     CGroup: /system.slice/csi-controller.service
             mq3050843 /usr/local/bin/nfcl-nas-csi-driver --v=4 --endpoint=unix:/etc/kubernetes/csi/csi.sock --controller=true --kubeconfig=/root/admin.kubeconfig --no>

Mar 16 17:01:46 rootless01m1 nfcl-nas-csi-driver[3050843]: I0316 17:01:46.208996 3050843 driver.go:125] Enabling volume access mode: MULTI_NODE_SINGLE_WRITER
Mar 16 17:01:46 rootless01m1 nfcl-nas-csi-driver[3050843]: I0316 17:01:46.209000 3050843 driver.go:125] Enabling volume access mode: MULTI_NODE_MULTI_WRITER
Mar 16 17:01:46 rootless01m1 nfcl-nas-csi-driver[3050843]: I0316 17:01:46.209006 3050843 driver.go:179] Enabling controller service capability: CREATE_DELETE_VOLUME
Mar 16 17:01:46 rootless01m1 nfcl-nas-csi-driver[3050843]: I0316 17:01:46.209012 3050843 driver.go:179] Enabling controller service capability: CREATE_DELETE_SNAPSHOT
Mar 16 17:01:46 rootless01m1 nfcl-nas-csi-driver[3050843]: I0316 17:01:46.209017 3050843 driver.go:179] Enabling controller service capability: LIST_SNAPSHOTS
Mar 16 17:01:46 rootless01m1 nfcl-nas-csi-driver[3050843]: I0316 17:01:46.259355 3050843 controller.go:115] PersistentVolume pvc-dfb76710-5552-4d14-aef0-96b881988299 a>
Mar 16 17:01:46 rootless01m1 nfcl-nas-csi-driver[3050843]: I0316 17:01:46.311367 3050843 main.go:92] Running driver...
Mar 16 17:01:46 rootless01m1 nfcl-nas-csi-driver[3050843]: I0316 17:01:46.311383 3050843 driver.go:215] Running driver: nas.csi.storage.nifcloud.com
```
# Start CSI Node by DaemonSet
```
# kubectl get pod -n nifcloud-nas-csi-driver -o wide
NAME                          READY   STATUS    RESTARTS   AGE    IP             NODE           NOMINATED NODE   READINESS GATES
nifcloud-nas-csi-node-5l54w   2/2     Running   0          112m   164.70.0.195   rootless01w1   <none>           <none>
nifcloud-nas-csi-node-dj7l6   2/2     Running   0          112m   164.70.8.161   rootless01w2   <none>           <none>
```
# Creating a Pod with PVC
```
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc
spec:
  accessModes:
  - ReadWriteMany
  resources:
    requests:
      storage: 10Gi
  storageClassName: csi-nifcloud-nas
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: alpine
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: alpine
  template:
    metadata:
      labels:
        app: alpine
    spec:
      containers:
      - command: ["tail","-f","/dev/null"]
        image: alpine
        name: alpine
        volumeMounts:
        - name: mnt
          mountPath: /mnt
      volumes:
      - name: mnt
        persistentVolumeClaim:
          claimName: test-pvc
```
```
# kubectl describe pvc test-pvc
Name:          test-pvc
Namespace:     default
StorageClass:  csi-nifcloud-nas
Status:        Bound
Volume:        pvc-dfb76710-5552-4d14-aef0-96b881988299
Labels:        <none>
Annotations:   pv.kubernetes.io/bind-completed: yes
               pv.kubernetes.io/bound-by-controller: yes
               volume.beta.kubernetes.io/storage-provisioner: nas.csi.storage.nifcloud.com
Finalizers:    [kubernetes.io/pvc-protection]
Capacity:      100Gi
Access Modes:  RWX
VolumeMode:    Filesystem
Used By:       alpine-59bcf8c7d6-b694f
Events:
  Type    Reason                 Age                 From                                                                            Message
  ----    ------                 ----                ----                                                                            -------
  Normal  Provisioning           46m                 nas.csi.storage.nifcloud.com_rootless01m1_03ba1654-64f7-44a1-8e76-089ecfa80412  External provisioner is provisioning volume for claim "default/test-pvc"
  Normal  ExternalProvisioning   35m (x43 over 46m)  persistentvolume-controller                                                     waiting for a volume to be created, either by external provisioner "nas.csi.storage.nifcloud.com" or manually created by system administrator
  Normal  ProvisioningSucceeded  35m                 nas.csi.storage.nifcloud.com_rootless01m1_03ba1654-64f7-44a1-8e76-089ecfa80412  Successfully provisioned volume pvc-dfb76710-5552-4d14-aef0-96b881988299
```
```
# kubectl exec -it alpine-59bcf8c7d6-b694f -- df -h /mnt
Filesystem                Size      Used Available Use% Mounted on
192.168.10.64:/          98.3G     60.0M     98.2G   0% /mnt
```