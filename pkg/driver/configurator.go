package driver

import (
	"fmt"
	"net"
	"strings"

	"github.com/golang/glog"
	"golang.org/x/net/context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"

	"github.com/nifcloud/nifcloud-sdk-go/service/computing"
	"github.com/ryo-watanabe/nifcloud-nas-csi-driver/pkg/cloud"
)

type NodeConfig struct {
	name      string
	publicIP  string
	privateIP string
}

type Configurator struct {
	driver     *NifcloudNasDriver
	kubeClient kubernetes.Interface
	cloud      cloud.Interface
	networkId  string
	zone       string
	nodes      []NodeConfig
	cidr       string
	ctx        context.Context
	chkIntvl   int64
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

func (c *Configurator) Init() error {
	err := c.installStorageClasses()
	if err != nil {
		return err
	}
	nodesUpdated, err := c.getNodeConfigs()
	if err != nil {
		return err
	}
	classesUpdated, err := c.getStorageClasses()
	if err != nil {
		return err
	}
	if !nodesUpdated && !classesUpdated {
		return nil
	}
	return c.configure(classesUpdated)
}

func (c *Configurator) Update() error {
	nodesUpdated, err := c.getNodeConfigs()
	if err != nil {
		return err
	}
	classesUpdated, err := c.getStorageClasses()
	if err != nil {
		return err
	}
	if !nodesUpdated && !classesUpdated {
		return nil
	}
	return c.configure(classesUpdated)
}

func (c *Configurator) runUpdate() {
	err := c.Update()
	if err != nil {
		runtime.HandleError(err)
	}
}

func (c *Configurator) configure(configureClasses bool) error {

	glog.V(4).Infof("Configurator starts to obtain private network settings from cloud")
	// Obtain private IPs, NetworkId and zone
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
			if configureClasses {
				c.networkId = pstr(cl.NetworkConfig.NetworkId)
				c.zone = zone
			}
			clusterName = *cl.Name
			break
		}
	}

	// Get computing instances
	if clusterName == "" {
		vms, err := c.cloud.ListInstances(c.ctx)
		if err != nil {
			return fmt.Errorf("getting vm list : %s", err.Error())
		}
		vmFound := 0
		networkId := ""
		zone := ""
		for _, vm := range vms {
			for i, nc := range c.nodes {
				if pstr(vm.IpAddress) == nc.publicIP {
					for _, nif := range vm.NetworkInterfaceSet {
						if pstr(nif.PrivateIpAddress) == pstr(vm.PrivateIpAddress) {
							networkId = pstr(nif.NiftyNetworkId)
						}
					}
					c.nodes[i].privateIP = pstr(vm.PrivateIpAddress)
					if vm.Placement != nil {
						zone = pstr(vm.Placement.AvailabilityZone)
					}
					vmFound++
				}
			}
		}
		if vmFound == len(c.nodes) && networkId != "" && zone != "" {
			if configureClasses {
				c.networkId = networkId
				c.zone = zone
			}
		} else {
			return fmt.Errorf("nodes not found in computing / hatoba")
		}
	} else {
		glog.V(4).Infof("Configurator : found corresponding cluster %s", clusterName)
	}

	glog.V(4).Infof("Configurator : private NetworkId=%s zone=%s", c.networkId, c.zone)
	for _, nc := range c.nodes {
		glog.V(4).Infof("Configurator : found node PublicIP=%s PrivateIP=%s", nc.publicIP, nc.privateIP)
	}

	if c.networkId == "net-COMMON_PRIVATE" {
		c.cidr = ""
	} else if configureClasses {

		// Get Private Lan and prepare recommended cidr divs
		lan, err := c.cloud.GetPrivateLan(c.ctx, c.networkId)
		if err != nil {
			return fmt.Errorf("getting private lan %s : %s", c.networkId, err.Error())
		}
		_, lanCidrBlk, err := net.ParseCIDR(pstr(lan.CidrBlock))
		if err != nil {
			return fmt.Errorf("parsing cidr block %s : %s", pstr(lan.CidrBlock), err.Error())
		}
		glog.V(4).Infof("Configurator : private LAN CIDR block %s", lanCidrBlk.String())
		lanMask, bits := lanCidrBlk.Mask.Size()
		offset := 4
		minmaskfordiv := 5
		for ; bits-lanMask-offset < minmaskfordiv; offset-- {}
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
			if pstr(db.NiftyNetworkId) == c.networkId {
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
			pls, ips, err := c.cloud.GetDhcpStatus(c.ctx, c.networkId, *r.RouterId)
			if err != nil {
				return fmt.Errorf("getting dhcp ip pool networkId:%s routerId:%s : %s",
					c.networkId, *r.RouterId, err.Error())
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

	err = c.setNodeConfigs()
	if err != nil {
		return fmt.Errorf("saving node configs : %s", err.Error())
	}

	if configureClasses {
		err = c.setStorageClasses()
		if err != nil {
			return fmt.Errorf("saving class configs : %s", err.Error())
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
			annotations = make(map[string]string, 0)
		}
		if annotations[c.driver.config.Name+"/privateIp"] == conf.privateIP {
			continue
		}
		annotations[c.driver.config.Name+"/privateIp"] = conf.privateIP
		csinode.SetAnnotations(annotations)
		csinode, err = c.kubeClient.StorageV1().CSINodes().Update(c.ctx, csinode, metav1.UpdateOptions{})
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
	c.networkId = ""

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
				if c.networkId == "" {
					c.networkId = cl.Parameters["networkId"]
				} else if c.networkId != cl.Parameters["networkId"] {
					// networkId not match between classes
					mustUpdate = true
				}
			}
			// Do not read cidr from storageclasses
		}
	}
	glog.V(4).Infof("Configurator : zone=%s networkID=%s", c.zone, c.networkId)
	if c.zone == "" || c.networkId == "" {
		mustUpdate = true
		glog.V(5).Infof("Configurator : storage classes must be updated")
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
		if cl.Parameters["zone"] == c.zone || cl.Parameters["networkId"] == c.networkId {
			continue
		}
		cl.Parameters["zone"] = c.zone
		cl.Parameters["networkId"] = c.networkId
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
		_, err = c.kubeClient.StorageV1().StorageClasses().Create(c.ctx, &cl, metav1.CreateOptions{})
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
		TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"},
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

func (d *CidrDivs) dump() {
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

func (d *CidrDivs) dropWithIPRange(ip_from, ip_to string) error {
	from := net.ParseIP(ip_from)
	if from == nil {
		return fmt.Errorf("error parsing ip %s", ip_from)
	}
	to := net.ParseIP(ip_to)
	if to == nil {
		return fmt.Errorf("erro parsing ip %s", ip_to)
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
