package driver

import (
	"fmt"
	"net"
	"sync"

	"github.com/golang/glog"
)

// Instance name holder

// InstanceNameHolder prohibits parallel operations of an instance
type InstanceNameHolder struct {
	creatingInstanceNames map[string]bool
	creatingInstanceMutex sync.Mutex
}

func newInstanceNameHolder() *InstanceNameHolder {
	names := make(map[string]bool)
	return &InstanceNameHolder{
		creatingInstanceNames: names,
	}
}

// SetCreating set an instance in the list
func (c *InstanceNameHolder) SetCreating(name string) error {
	c.creatingInstanceMutex.Lock()
	defer c.creatingInstanceMutex.Unlock()
	if _, ok := c.creatingInstanceNames[name]; ok {
		return fmt.Errorf("Instance %s is about to create in other Create request", name)
	}
	c.creatingInstanceNames[name] = true
	return nil
}

// IsCreating returns wheather there's a creating instance with the name
func (c *InstanceNameHolder) IsCreating(name string) bool {
	_, ok := c.creatingInstanceNames[name]
	return ok
}

// UnsetCreating unsets an instance from the list
func (c *InstanceNameHolder) UnsetCreating(name string) {
	c.creatingInstanceMutex.Lock()
	defer c.creatingInstanceMutex.Unlock()
	delete(c.creatingInstanceNames, name)
}

// ip allocator

const (
	byteMax = 255
)

// IPAllocator struct consists of shared resources that are used
// to keep track of the /29 IPRanges currently reserved by service instances
type IPAllocator struct {
	pendingIPs      map[string]bool
	pendingIPsMutex sync.Mutex
}

// NewIPAllocator is the constructor to initialize the IPAllocator object
func newIPAllocator(pendingIPs map[string]bool) *IPAllocator {
	pendingIPsCopy := make(map[string]bool)
	for pendingIP := range pendingIPs {
		pendingIPsCopy[pendingIP] = true
	}
	return &IPAllocator{
		pendingIPs: pendingIPsCopy,
	}
}

// holdIPs adds a particular IP in the pendingIPs set
func (ipAllocator *IPAllocator) holdIP(ip string) {
	ipAllocator.pendingIPs[ip] = true
}

// ReleaseIP releases the pending IP
func (ipAllocator *IPAllocator) ReleaseIP(ip string) {
	ipAllocator.pendingIPsMutex.Lock()
	defer ipAllocator.pendingIPsMutex.Unlock()
	delete(ipAllocator.pendingIPs, ip)
}

// GetUnreservedIP returns an unreserved IP.
func (ipAllocator *IPAllocator) GetUnreservedIP(
	cidr string, cloudInstancesReservedIPs map[string]bool) (string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	var reservedIPs = make(map[string]bool)

	// The final reserved list is obtained
	// by combining the cloudInstancesReservedIPRanges list and the pendingIPRanges list in the ipAllocator
	for cloudInstancesReservedIP := range cloudInstancesReservedIPs {
		reservedIPs[cloudInstancesReservedIP] = true
	}

	// Lock is placed here so that the pendingIPRanges list captures all the IPs pending reservation in the cloud instances
	ipAllocator.pendingIPsMutex.Lock()
	defer ipAllocator.pendingIPsMutex.Unlock()
	for reservedIP := range ipAllocator.pendingIPs {
		reservedIPs[reservedIP] = true
	}

	for targetIP := cloneIP(ip.Mask(ipnet.Mask)); ipnet.Contains(targetIP) && err == nil; targetIP, err = incrementIP(targetIP, 1) { //nolint:lll
		used := false
		for reservedIP := range reservedIPs {
			if targetIP.Equal(net.ParseIP(reservedIP)) {
				used = true
			}
		}
		if !used {
			ipAllocator.holdIP(targetIP.String())
			return targetIP.String(), nil
		}
	}

	// No unreserved IP available in the CIDR range since we did not return
	return "", fmt.Errorf("all of the IPs in the cidr %s are reserved", cidr)
}

// Increment the given IP value by the provided step. The step is a byte
func incrementIP(ip net.IP, step byte) (net.IP, error) {
	incrementedIP := cloneIP(ip)
	incrementedIP = incrementedIP.To4()

	// Step can be added directly to the Least Significant Byte and we can return the result
	if incrementedIP[3] <= byteMax-step {
		incrementedIP[3] += step
		return incrementedIP, nil
	}

	// Step addition in the Least Significant Byte resulted in overflow
	// Propogating the carry addition to the higher order bytes and calculating value of the current byte
	incrementedIP[3] = incrementedIP[3] - byteMax + step - 1

	for ipByte := 2; ipByte >= 0; ipByte-- {
		// Rollover occurs when value changes from maximum byte value to 0 as propagated carry is 1
		if incrementedIP[ipByte] != byteMax {
			incrementedIP[ipByte]++
			return incrementedIP, nil
		}
		incrementedIP[ipByte] = 0
	}
	return nil, fmt.Errorf("ip range overflowed while incrementing IP %s by step %d", ip.String(), step)
}

// Clone the provided IP and return the copy
func cloneIP(ip net.IP) net.IP {
	clone := make(net.IP, len(ip))
	copy(clone, ip)
	return clone
}

// resource_queue

// OperateResourceQueue provides a queue for creating/deleting resources
type OperateResourceQueue struct {
	name       string
	resources  map[string]bool
	queueMutex sync.Mutex
}

func newOperateResourceQueue(name string) *OperateResourceQueue {
	rs := make(map[string]bool)
	return &OperateResourceQueue{
		name:      name,
		resources: rs,
	}
}

// Queue pushes a resource to the queue
func (q *OperateResourceQueue) Queue(name string) error {
	if _, ok := q.resources[name]; ok {
		return fmt.Errorf("Duplicate Queue called for %s in %s", name, q.name)
	}
	q.resources[name] = false
	q.queueMutex.Lock()
	q.resources[name] = true
	return nil
}

// Show prints the queue
func (q *OperateResourceQueue) Show() {
	if len(q.resources) == 0 {
		glog.V(4).Infof("No resources Operating/Pending in %s", q.name)
	}
	for name, operating := range q.resources {
		state := "Pending"
		if operating {
			state = "Operating"
		}
		glog.V(4).Infof("Resource %s %s in %s", name, state, q.name)
	}
}

// UnsetQueue pops a resource from the queue
func (q *OperateResourceQueue) UnsetQueue(name string) {
	operating, ok := q.resources[name]
	if !ok || !operating {
		return
	}
	delete(q.resources, name)
	q.queueMutex.Unlock()
}
