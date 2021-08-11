package driver

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang/glog"
	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
	"golang.org/x/net/context"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/nifcloud/nifcloud-sdk-go/service/computing"
	"github.com/ryo-watanabe/nifcloud-nas-csi-driver/pkg/cloud"
)

// NodeConfig holds IPs of a node
type NodeConfig struct {
	name      string
	publicIP  string
	privateIP string
}

// Configurator sets private LAN configurations for CSI
type Configurator struct {
	driver      *NifcloudNasDriver
	kubeClient  kubernetes.Interface
	cloud       cloud.Interface
	networkID   string
	zone        string
	nodes       []NodeConfig
	cidr        string
	clusterName string
	ctx         context.Context
	chkIntvl    int64
}

func newConfigurator(driver *NifcloudNasDriver) *Configurator {
	return &Configurator{
		driver:     driver,
		kubeClient: driver.config.KubeClient,
		cloud:      driver.config.Cloud,
		ctx:        context.TODO(),
		chkIntvl:   300,
	}
}

// Init initializes configurations
func (c *Configurator) Init() error {
	err := c.installStorageClasses()
	if err != nil {
		return err
	}
	if c.driver.config.CfgSnapRepo {
		err = c.installSnapshotClasses()
		if err != nil {
			return err
		}
	}
	return c.Update()
}

// Update updates configurations if necessary
func (c *Configurator) Update() error {
	nodesUpdate, err := c.getNodeConfigs()
	if err != nil {
		return err
	}
	classesUpdate, err := c.getStorageClasses()
	if err != nil {
		return err
	}
	snapClassUpdate := false
	if c.driver.config.CfgSnapRepo {
		snapClassUpdate, err = c.getSnapshotClasses()
		if err != nil {
			return err
		}
	}
	if !nodesUpdate && !classesUpdate && !snapClassUpdate {
		return nil
	}
	return c.configure(classesUpdate, snapClassUpdate)
}

func (c *Configurator) runUpdate() {
	err := c.Update()
	if err != nil {
		runtime.HandleError(err)
	}
}

func (c *Configurator) configure(configureClasses bool, configureSnapClass bool) error {

	glog.V(4).Infof("Configurator starts to obtain private network settings from cloud")

	// Obtain private IPs, NetworkId and zone

	if c.driver.config.Hatoba {
		// Get hatoba nodes
		clusters, err := c.cloud.ListClusters(c.ctx)
		if err != nil {
			return fmt.Errorf("getting cluster list : %s", err.Error())
		}
		clusterName := ""
		for _, cl := range clusters {
			vmFound := 0
			zone := ""
			for _, p := range cl.NodePools {
				for _, n := range p.Nodes {
					for i, nc := range c.nodes {
						if pstr(n.PublicIpAddress) == nc.publicIP {
							vmFound++
							c.nodes[i].privateIP = pstr(n.PrivateIpAddress)
							zone = pstr(n.AvailabilityZone)
						}
					}
				}
			}
			if vmFound == len(c.nodes) {
				clusterName = *cl.Name
				if configureClasses || configureSnapClass {
					c.networkID = pstr(cl.NetworkConfig.NetworkId)
					c.zone = zone
					c.clusterName = clusterName
				}
				break
			}
		}
		if clusterName != "" {
			glog.V(4).Infof("Configurator : found corresponding cluster %s", clusterName)
		} else {
			return fmt.Errorf("nodes not found in hatoba")
		}
	} else {
		// Get computing instances
		vms, err := c.cloud.ListInstances(c.ctx)
		if err != nil {
			return fmt.Errorf("getting vm list : %s", err.Error())
		}
		vmFound := 0
		networkID := ""
		zone := ""
		clusterName := ""
		for _, vm := range vms {
			for i, nc := range c.nodes {
				if pstr(vm.IpAddress) == nc.publicIP {
					for _, nif := range vm.NetworkInterfaceSet {
						if pstr(nif.PrivateIpAddress) == pstr(vm.PrivateIpAddress) {
							networkID = pstr(nif.NiftyNetworkId)
						}
					}
					c.nodes[i].privateIP = pstr(vm.PrivateIpAddress)
					if vm.Placement != nil {
						zone = pstr(vm.Placement.AvailabilityZone)
					}
					if clusterName != "" {
						clusterName += "-"
					}
					clusterName += pstr(vm.InstanceId)
					vmFound++
				}
			}
		}
		if vmFound == len(c.nodes) && networkID != "" && zone != "" {
			if configureClasses || configureSnapClass {
				c.networkID = networkID
				c.zone = zone
				c.clusterName = clusterName
			}
		} else {
			return fmt.Errorf("nodes not found in computing")
		}
	}

	glog.V(4).Infof("Configurator : private NetworkId=%s zone=%s", c.networkID, c.zone)
	for _, nc := range c.nodes {
		glog.V(4).Infof("Configurator : found node PublicIP=%s PrivateIP=%s", nc.publicIP, nc.privateIP)
	}

	if c.networkID == "net-COMMON_PRIVATE" {
		c.cidr = ""

	} else if configureClasses && c.driver.config.CidrBlkRcmd {

		// Experimental : Cidr block recommendation

		// Get Private Lan and prepare recommended cidr divs
		lan, err := c.cloud.GetPrivateLan(c.ctx, c.networkID)
		if err != nil {
			return fmt.Errorf("getting private lan %s : %s", c.networkID, err.Error())
		}
		_, lanCidrBlk, err := net.ParseCIDR(pstr(lan.CidrBlock))
		if err != nil {
			return fmt.Errorf("parsing cidr block %s : %s", pstr(lan.CidrBlock), err.Error())
		}
		glog.V(4).Infof("Configurator : private LAN CIDR block %s", lanCidrBlk.String())
		lanMask, bits := lanCidrBlk.Mask.Size()
		offset := 4
		minmaskfordiv := 5
		for ; bits-lanMask-offset < minmaskfordiv; offset-- {
		}
		rcmdCidrBlks, err := newCidrDivs(lanCidrBlk, offset)
		if err != nil {
			return fmt.Errorf("initialing recommended cidr divs : %s", err.Error())
		}
		if offset >= 3 {
			err = rcmdCidrBlks.dropHeadAndTail()
			if err != nil {
				return fmt.Errorf("dropping recommended cidr divs : %s", err.Error())
			}
		}

		// Get RDB IPs and drop divs contains them
		dbs, err := c.cloud.ListRdbInstances(c.ctx)
		if err != nil {
			return fmt.Errorf("getting RDB list : %s", err.Error())
		}
		for _, db := range dbs {
			if pstr(db.NiftyNetworkId) == c.networkID {
				glog.V(4).Infof("Configurator : dropping CIDR div with rdb IP %s", pstr(db.NiftyMasterPrivateAddress))
				err = rcmdCidrBlks.dropWithIP(pstr(db.NiftyMasterPrivateAddress))
				if err != nil {
					return fmt.Errorf("dropping recommended cidr divs : %s", err.Error())
				}
			}
		}

		// Get DHCP status
		ipPools := []computing.IpAddressPoolSet{}
		staticIps := []computing.DhcpIpAddressSet{}

		for _, r := range lan.RouterSet {
			pls, ips, err := c.cloud.GetDhcpStatus(c.ctx, c.networkID, *r.RouterId)
			if err != nil {
				return fmt.Errorf("getting dhcp ip pool networkId:%s routerId:%s : %s",
					c.networkID, *r.RouterId, err.Error())
			}
			ipPools = append(ipPools, pls...)
			staticIps = append(staticIps, ips...)
		}
		for _, pool := range ipPools {
			glog.V(4).Infof("Configurator : dropping CIDR div with DHCP pool %s - %s",
				pstr(pool.StartIpAddress), pstr(pool.StopIpAddress))
			err = rcmdCidrBlks.dropWithIPRange(pstr(pool.StartIpAddress), pstr(pool.StopIpAddress))
			if err != nil {
				return fmt.Errorf("dropping recommended cidr divs : %s", err.Error())
			}
		}
		for _, ip := range staticIps {
			glog.V(4).Infof("Configurator : dropping CIDR div with static IP %s", pstr(ip.IpAddress))
			err = rcmdCidrBlks.dropWithIP(pstr(ip.IpAddress))
			if err != nil {
				return fmt.Errorf("dropping recommended cidr divs : %s", err.Error())
			}
		}

		// determine cidr
		if len(rcmdCidrBlks.divs) == 0 {
			c.cidr = ""
		} else {
			c.cidr = rcmdCidrBlks.divs[len(rcmdCidrBlks.divs)/2].String()
		}
		glog.V(4).Infof("Configurator : determined recommended CIDR block for NAS %s", c.cidr)
	}

	err := c.setNodeConfigs()
	if err != nil {
		return fmt.Errorf("saving node configs : %s", err.Error())
	}

	if configureClasses {
		err = c.setStorageClasses()
		if err != nil {
			return fmt.Errorf("saving class configs : %s", err.Error())
		}
	}

	if configureSnapClass {
		err = c.setSnapshotClasses()
		if err != nil {
			return fmt.Errorf("saving snapshot secret : %s", err.Error())
		}
	}

	return nil
}

func pstr(pstr *string) string {
	if pstr == nil {
		return ""
	}
	return *pstr
}

func (c *Configurator) getNodeConfigs() (bool, error) {
	mustUpdate := false
	nodeConfigs := []NodeConfig{}

	// Get v1.nodes
	nodes, err := c.kubeClient.CoreV1().Nodes().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return mustUpdate, fmt.Errorf("getting nodes in GetNodeConfigs : %s", err.Error())
	}
	for _, n := range nodes.Items {
		nc := NodeConfig{name: n.GetName()}
		for _, addr := range n.Status.Addresses {
			if addr.Type == corev1.NodeExternalIP || addr.Type == corev1.NodeInternalIP {
				if isPrivate(addr.Address) {
					nc.privateIP = addr.Address
				} else {
					nc.publicIP = addr.Address
				}
			}
		}
		if nc.publicIP == "" {
			return mustUpdate, fmt.Errorf("GetNodeConfigs : node %s does not have public IP", nc.name)
		}
		nodeConfigs = append(nodeConfigs, nc)
	}

	// Get storagev1.csinodes
	csinodes, err := c.kubeClient.StorageV1().CSINodes().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return mustUpdate, fmt.Errorf("getting csinodes in GetNodeConfigs : %s", err.Error())
	}

	// Check csinodes' annotation
	csiNodeConfigs := []NodeConfig{}
	for _, conf := range nodeConfigs {
		for _, csinode := range csinodes.Items {
			if conf.name == csinode.GetName() {
				for _, d := range csinode.Spec.Drivers {
					if d.Name == c.driver.config.Name {
						annPrivIP := csinode.GetAnnotations()[c.driver.config.Name+"/privateIp"]
						if annPrivIP == "" || (conf.privateIP != "" && annPrivIP != conf.privateIP) {
							mustUpdate = true
						} else {
							conf.privateIP = annPrivIP
						}
						csiNodeConfigs = append(csiNodeConfigs, conf)
					}
				}
			}
		}
	}

	// Update node configs
	c.nodes = csiNodeConfigs
	for _, nc := range c.nodes {
		glog.V(5).Infof("Configurator : Name=%s PublicIP=%s PrivateIP=%s", nc.name, nc.publicIP, nc.privateIP)
	}
	if mustUpdate {
		glog.V(4).Infof("Configurator : node configs must be updated")
	}

	return mustUpdate, nil
}

func (c *Configurator) setNodeConfigs() error {
	// Update storagev1.csinodes
	for _, conf := range c.nodes {
		csinode, err := c.kubeClient.StorageV1().CSINodes().Get(c.ctx, conf.name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("getting csinode in SetNodeConfigs : %s", err.Error())
		}
		annotations := csinode.ObjectMeta.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		if annotations[c.driver.config.Name+"/privateIp"] == conf.privateIP {
			continue
		}
		annotations[c.driver.config.Name+"/privateIp"] = conf.privateIP
		csinode.SetAnnotations(annotations)
		_, err = c.kubeClient.StorageV1().CSINodes().Update(c.ctx, csinode, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("updating csinode in SetNodeConfigs : %s", err.Error())
		}
	}

	glog.V(4).Infof("Configurator : csinodes updated")
	return nil
}

func (c *Configurator) getStorageClasses() (bool, error) {
	mustUpdate := false
	c.zone = ""
	c.networkID = ""

	// Get storagev1.storageclasses
	cls, err := c.kubeClient.StorageV1().StorageClasses().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return mustUpdate, fmt.Errorf("getting storageclasses in GetStorageClasses : %s", err.Error())
	}
	for _, cl := range cls.Items {
		if cl.Provisioner == c.driver.config.Name {
			if cl.Parameters["zone"] == "" {
				// zone not set
				mustUpdate = true
			} else {
				if c.zone == "" {
					c.zone = cl.Parameters["zone"]
				} else if c.zone != cl.Parameters["zone"] {
					// zone not match between classes
					mustUpdate = true
				}
			}
			if cl.Parameters["networkId"] == "" {
				// networkId not set
				mustUpdate = true
			} else {
				if c.networkID == "" {
					c.networkID = cl.Parameters["networkId"]
				} else if c.networkID != cl.Parameters["networkId"] {
					// networkId not match between classes
					mustUpdate = true
				}
			}
			// Do not read cidr from storageclasses
		}
	}
	glog.V(5).Infof("Configurator : zone=%s networkID=%s", c.zone, c.networkID)
	if c.zone == "" || c.networkID == "" {
		mustUpdate = true
		glog.V(4).Infof("Configurator : storage classes must be updated")
	}

	return mustUpdate, nil
}

func (c *Configurator) setStorageClasses() error {

	cls, err := c.kubeClient.StorageV1().StorageClasses().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("getting storageclasses in SetStorageClasses : %s", err.Error())
	}
	// Update storagev1.storageclasses
	for _, cl := range cls.Items {
		if cl.Provisioner != c.driver.config.Name {
			continue
		}
		if cl.Parameters["zone"] == c.zone && cl.Parameters["networkId"] == c.networkID {
			continue
		}
		cl.Parameters["zone"] = c.zone
		cl.Parameters["networkId"] = c.networkID
		if cl.Parameters["description"] == "" && c.clusterName != "" {
			cl.Parameters["description"] = "csi-volume of cluster:" + c.clusterName
		}
		// Do not overwrite existing cidr
		if c.cidr != "" && cl.Parameters["reservedIpv4Cidr"] == "" {
			cl.Parameters["reservedIpv4Cidr"] = c.cidr
		}
		err = c.kubeClient.StorageV1().StorageClasses().Delete(c.ctx, cl.GetName(), metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("deleting storageclass in SetStorageClasses : %s", err.Error())
		}
		cl.SetResourceVersion("")
		cl.SetUID("")
		copy := cl.DeepCopy()
		_, err = c.kubeClient.StorageV1().StorageClasses().Create(c.ctx, copy, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating storageclass in SetStorageClasses : %s", err.Error())
		}
	}

	glog.V(4).Infof("Configurator : classes updated")
	return nil
}

func (c *Configurator) installStorageClasses() error {

	// Check with templates
	standard := true
	hispeed := true
	shared := true
	sharedhi := true
	cls, err := c.kubeClient.StorageV1().StorageClasses().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("getting storageclasses in installStorageClasses : %s", err.Error())
	}
	for _, cl := range cls.Items {
		if cl.Provisioner != c.driver.config.Name {
			continue
		}
		if cl.Parameters["instanceType"] == "0" && cl.Parameters["shared"] != "true" {
			standard = false
		}
		if cl.Parameters["instanceType"] == "1" && cl.Parameters["shared"] != "true" {
			hispeed = false
		}
		if cl.Parameters["instanceType"] == "0" && cl.Parameters["shared"] == "true" {
			shared = false
		}
		if cl.Parameters["instanceType"] == "1" && cl.Parameters["shared"] == "true" {
			sharedhi = false
		}
	}

	// Create classes
	newClass := &storagev1.StorageClass{
		TypeMeta:    metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"},
		Provisioner: c.driver.config.Name,
	}
	if standard {
		newClass.SetName("csi-nifcloud-nas-std")
		newClass.Parameters = map[string]string{"instanceType": "0"}
		_, err = c.kubeClient.StorageV1().StorageClasses().Create(c.ctx, newClass, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating storageclass in installStorageClasses : %s", err.Error())
		}
		glog.V(4).Infof("Configurator : storageclass csi-nifcloud-nas-std installed")
	}
	if hispeed {
		newClass.SetName("csi-nifcloud-nas-hi")
		newClass.Parameters = map[string]string{"instanceType": "1"}
		_, err = c.kubeClient.StorageV1().StorageClasses().Create(c.ctx, newClass, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating storageclass in installStorageClasses : %s", err.Error())
		}
		glog.V(4).Infof("Configurator : storageclass csi-nifcloud-nas-hi installed")
	}
	if shared {
		newClass.SetName("csi-nifcloud-nas-shrd")
		newClass.Parameters = map[string]string{"instanceType": "0", "shared": "true", "capacityParInstanceGiB": "500"}
		_, err = c.kubeClient.StorageV1().StorageClasses().Create(c.ctx, newClass, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating storageclass in installStorageClasses : %s", err.Error())
		}
		glog.V(4).Infof("Configurator : storageclass csi-nifcloud-nas-shrd installed")
	}
	if sharedhi {
		newClass.SetName("csi-nifcloud-nas-shrdhi")
		newClass.Parameters = map[string]string{"instanceType": "1", "shared": "true", "capacityParInstanceGiB": "1000"}
		_, err = c.kubeClient.StorageV1().StorageClasses().Create(c.ctx, newClass, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating storageclass in installStorageClasses : %s", err.Error())
		}
		glog.V(4).Infof("Configurator : storageclass csi-nifcloud-nas-shrdhi installed")
	}

	return nil
}

func (c *Configurator) getSnapshotClasses() (bool, error) {

	mustUpdate := true
	// Get CR snapshotv1.volumesnapshotclasses
	cls, err := c.driver.config.SnapClient.SnapshotV1().VolumeSnapshotClasses().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("getting snapshotclasses in getSnapshotClasses : %s", err.Error())
	}
	for _, cl := range cls.Items {
		if cl.Driver != c.driver.config.Name {
			continue
		}
		secrets, err := getSecretsFromSnapshotClass(c.ctx, cl.GetName(), c.driver)
		if err == nil && secrets["resticRepository"] != "" && secrets["resticPassword"] != "" {
			mustUpdate = false
		}
	}
	if mustUpdate {
		glog.V(4).Infof("Configurator : snapshot classes must be updated")
	}
	return mustUpdate, nil
}

func regionFromEndpoint(ep string) string {
	ep = strings.TrimPrefix(ep, "https://")
	ep = strings.TrimPrefix(ep, "http://")
	domains := strings.Split(ep, ".")
	if len(domains) >= 1 {
		return domains[0]
	}
	return ""
}

func (c *Configurator) createBucket(bucketname string, secret *corev1.Secret) error {
	// creds
	accesskey := os.Getenv("AWS_ACCESS_KEY_ID")
	if accesskey == "" {
		return fmt.Errorf("Configurator : cannot get accesskey from env")
	}
	secretkey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if secretkey == "" {
		return fmt.Errorf("Configurator : cannot get secretkey from env")
	}
	// session
	region := regionFromEndpoint(c.driver.config.DefaultSnapEndpoint)
	glog.V(4).Infof(
		"Configurator : initializing session - region:%s, endpoint:%s",
		region, c.driver.config.DefaultSnapEndpoint,
	)
	creds := credentials.NewStaticCredentials(accesskey, secretkey, "")
	sess, _ := session.NewSession(&aws.Config{
		Credentials: creds,
		Region:      aws.String(region),
		Endpoint:    aws.String(c.driver.config.DefaultSnapEndpoint),
	})
	// create bucket
	svc := s3.New(sess)
	_, err := svc.CreateBucket(&s3.CreateBucketInput{Bucket: aws.String(bucketname)})
	if err != nil {
		return err
	}

	// prerare file for store secret
	secretFile, err := os.Create("/tmp/" + secret.GetName() + ".tgz")
	if err != nil {
		return fmt.Errorf("Creating tgz file failed : %s", err.Error())
	}
	tgz := gzip.NewWriter(secretFile)
	defer func() { _ = tgz.Close() }()
	tarWriter := tar.NewWriter(tgz)
	defer func() { _ = tarWriter.Close() }()
	// Store secret resource as json
	secretResource, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("Marshalling json failed : %s", err.Error())
	}
	hdr := &tar.Header{
		Name:     filepath.Join("/", secret.GetName()+".json"),
		Size:     int64(len(secretResource)),
		Typeflag: tar.TypeReg,
		Mode:     0755,
		ModTime:  time.Now(),
	}
	if err := tarWriter.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar writer json file header failed : %s", err.Error())
	}
	if _, err := tarWriter.Write(secretResource); err != nil {
		return fmt.Errorf("tar writer json content failed : %s", err.Error())
	}
	_ = tarWriter.Close()
	_ = tgz.Close()
	_ = secretFile.Close()
	// upload secret json tgz
	uploadFile, err := os.Open("/tmp/" + secret.GetName() + ".tgz")
	defer func() { _ = uploadFile.Close() }()
	if err != nil {
		return fmt.Errorf("re-openning tgz file failed : %s", err.Error())
	}
	uploader := s3manager.NewUploader(sess)
	_, err = uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(bucketname),
		Key:    aws.String(secret.GetName() + ".tgz"),
		Body:   uploadFile,
	})
	if err != nil {
		return fmt.Errorf("Error uploading %s to bucket %s : %s", secret.GetName()+".tgz", bucketname, err.Error())
	}
	return nil
}

func (c *Configurator) setSnapshotClasses() error {

	clusterID := ""
	if c.clusterName != "" {
		clusterID = c.clusterName + "-"
	}
	clusterUID, err := getClusterUID(c.ctx, c.driver)
	if err != nil {
		return fmt.Errorf("Error getting namespace UID : %s", err.Error())
	}
	clusterID += clusterUID
	bucketname := "csi-snapshot-" + rand.String(5)
	repository := "s3:" + c.driver.config.DefaultSnapEndpoint + "/" + bucketname
	password := makePassword(clusterUID, 16)

	cls, err := c.driver.config.SnapClient.SnapshotV1().VolumeSnapshotClasses().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("getting storageclasses in setSnapshotClasses : %s", err.Error())
	}
	// Create a secret for the snapshotclass
	for _, cl := range cls.Items {
		if cl.Driver != c.driver.config.Name {
			continue
		}
		secrets, err := getSecretsFromSnapshotClass(c.ctx, cl.GetName(), c.driver)
		if err == nil && secrets["resticRepository"] != "" && secrets["resticPassword"] != "" {
			continue
		}
		if cl.Parameters["csi.storage.k8s.io/snapshotter-secret-name"] != "restic-creds" {
			continue
		}
		if cl.Parameters["csi.storage.k8s.io/snapshotter-secret-namespace"] != "nifcloud-nas-csi-driver" {
			continue
		}
		// delete existing secret
		_, err = c.kubeClient.CoreV1().Secrets("nifcloud-nas-csi-driver").Get(
			c.ctx, "restic-creds", metav1.GetOptions{})
		if err == nil {
			err := c.kubeClient.CoreV1().Secrets("nifcloud-nas-csi-driver").Delete(
				c.ctx, "restic-creds", metav1.DeleteOptions{})
			if err != nil {
				return fmt.Errorf("deleting secret in setSnapshotClasses : %s", err.Error())
			}
		}
		// create new secret
		newSecret := &corev1.Secret{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "restic-creds",
				Annotations: map[string]string{
					c.driver.config.Name + "/clusterID": clusterID,
				},
			},
			StringData: map[string]string{
				"resticRepository": repository,
				"resticPassword":   password,
			},
		}
		createdSecret, err := c.kubeClient.CoreV1().Secrets("nifcloud-nas-csi-driver").Create(
			c.ctx, newSecret, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("Error creating secret in setSnapshotClasses : %s", err.Error())
		}
		// create new bucket and store the secret
		err = c.createBucket(bucketname, createdSecret)
		if err != nil {
			return fmt.Errorf("Error creating bucket in setSnapshotClasses : %s", err.Error())
		}
		break
	}

	glog.V(4).Infof("Configurator : snapshot classes updated")
	return nil
}

func (c *Configurator) installSnapshotClasses() error {

	// Get CR snapshotv1.snapshotclasses
	cls, err := c.driver.config.SnapClient.SnapshotV1().VolumeSnapshotClasses().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("getting snapshotclasses in installSnapshotClasses : %s", err.Error())
	}
	found := false
	for _, cl := range cls.Items {
		if cl.Driver == c.driver.config.Name {
			found = true
			break
		}
	}
	if !found {
		// Create a class
		newClass := &snapshotv1.VolumeSnapshotClass{
			TypeMeta:       metav1.TypeMeta{APIVersion: "snapshot.storage.k8s.io/v1", Kind: "VolumeStorageClass"},
			ObjectMeta:     metav1.ObjectMeta{Name: "csi-nifcloud-nas-restic"},
			Driver:         c.driver.config.Name,
			DeletionPolicy: "Delete",
			Parameters: map[string]string{
				"csi.storage.k8s.io/snapshotter-secret-name":      "restic-creds",
				"csi.storage.k8s.io/snapshotter-secret-namespace": "nifcloud-nas-csi-driver",
			},
		}
		_, err = c.driver.config.SnapClient.SnapshotV1().VolumeSnapshotClasses().Create(
			c.ctx, newClass, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating snapshotclass in installSnapshotClasses : %s", err.Error())
		}
		glog.V(4).Infof("Configurator : snapshotclass csi-nifcloud-nas-restic installed")
	}
	return nil
}

// IP utils

func isPrivate(ipstr string) bool {
	if strings.HasPrefix(ipstr, "192.168.") || strings.HasPrefix(ipstr, "10.") {
		return true
	}
	if strings.HasPrefix(ipstr, "172.") {
		ip := net.ParseIP(ipstr)
		if ip != nil {
			_, cidr, _ := net.ParseCIDR("172.16.0.0/12")
			if cidr.Contains(ip) {
				return true
			}
		}
	}
	return false
}

func dupIP(ip net.IP) net.IP {
	dup := make(net.IP, len(ip))
	copy(dup, ip)
	return dup
}

func startIP(cidr *net.IPNet) net.IP {
	start := dupIP(cidr.IP)
	for i := 0; i < len(cidr.IP); i++ {
		start[i] &= cidr.Mask[i]
	}
	return start
}

func endIP(cidr *net.IPNet) net.IP {
	end := startIP(cidr)
	for i := 0; i < len(cidr.IP); i++ {
		end[i] += ^cidr.Mask[i]
	}
	return end
}

func incIP(ip net.IP) net.IP {
	inc := dupIP(ip)
	for j := len(ip) - 1; j >= 0; j-- {
		inc[j]++
		if inc[j] > 0 {
			break
		}
	}
	return inc
}

func divideNet(cidr *net.IPNet, maskOffset int) ([]*net.IPNet, error) {
	divs := []*net.IPNet{}
	if maskOffset <= 0 {
		return divs, fmt.Errorf("mask offset must be positive value")
	}
	divNum := 1
	for i := 0; i < maskOffset; i++ {
		divNum *= 2
	}
	ones, bit := cidr.Mask.Size()
	divMask := net.CIDRMask(ones+maskOffset, bit)
	if maskOffset > bit-ones {
		return divs, fmt.Errorf("mask offset must not be greater than %d", bit-ones)
	}
	start := startIP(cidr)
	for i := 0; i < divNum; i++ {
		div := &net.IPNet{IP: start, Mask: divMask}
		start = incIP(endIP(div))
		divs = append(divs, div)
	}
	return divs, nil
}

// CidrDivs holds informations for dividing a network IP range
type CidrDivs struct {
	cidr       net.IPNet
	maskOffset int
	divs       []*net.IPNet
}

func newCidrDivs(cidr *net.IPNet, maskOffset int) (*CidrDivs, error) {
	divs, err := divideNet(cidr, maskOffset)
	if err != nil {
		return nil, err
	}
	return &CidrDivs{
		cidr:       *cidr,
		maskOffset: maskOffset,
		divs:       divs,
	}, nil
}

func (d *CidrDivs) dump() { //nolint:unused
	fmt.Printf("--- divs : %d\n", len(d.divs))
	for i, div := range d.divs {
		fmt.Printf("%d : %s (%s - %s)\n", i, div.String(), startIP(div), endIP(div))
	}
}

func (d *CidrDivs) dropHeadAndTail() error {
	if len(d.divs) < 3 {
		return fmt.Errorf("no enough divs num=%d", len(d.divs))
	}
	d.divs = d.divs[1 : len(d.divs)-1]
	return nil
}

func (d *CidrDivs) dropWithIP(ipstr string) error {
	ip := net.ParseIP(ipstr)
	if ip == nil {
		return fmt.Errorf("error parsing ip %s", ipstr)
	}
	index := -1
	for i := 0; i < len(d.divs); i++ {
		if d.divs[i].Contains(ip) {
			index = i
			break
		}
	}
	if index >= 0 {
		if index == 0 {
			d.divs = d.divs[1:len(d.divs)]
		} else if index == len(d.divs)-1 {
			d.divs = d.divs[0 : len(d.divs)-1]
		} else {
			d.divs = append(d.divs[0:index], d.divs[index+1:len(d.divs)]...)
		}
	}
	return nil
}

func compareIP(a, b net.IP) int {
	a = a.To16()
	b = b.To16()
	for i := 0; i < len(a); i++ {
		if a[i] < b[i] {
			return 1
		}
		if a[i] > b[i] {
			return -1
		}
	}
	return 0
}

func isDevInRange(from, to net.IP, div *net.IPNet) bool {
	if div.Contains(from) || div.Contains(to) {
		return true
	}
	if compareIP(from, startIP(div)) > 0 && compareIP(endIP(div), to) > 0 {
		return true
	}
	return false
}

func (d *CidrDivs) dropWithIPRange(ipFrom, ipTo string) error {
	from := net.ParseIP(ipFrom)
	if from == nil {
		return fmt.Errorf("error parsing ip %s", ipFrom)
	}
	to := net.ParseIP(ipTo)
	if to == nil {
		return fmt.Errorf("erro parsing ip %s", ipTo)
	}
	divFrom := -1
	divTo := -1
	inRange := false
	for i := 0; i < len(d.divs); i++ {
		if isDevInRange(from, to, d.divs[i]) {
			if !inRange {
				divFrom = i
				inRange = true
			}
			divTo = i
		}
	}
	if inRange {
		if divFrom == 0 {
			d.divs = d.divs[divTo+1 : len(d.divs)]
		} else if divFrom == len(d.divs)-1 {
			d.divs = d.divs[0 : len(d.divs)-1]
		} else {
			d.divs = append(d.divs[0:divFrom], d.divs[divTo+1:len(d.divs)]...)
		}
	}
	return nil
}

func validateCIDRIP(cidrip string) error {
	_, _, err := net.ParseCIDR(cidrip)
	return err
}
