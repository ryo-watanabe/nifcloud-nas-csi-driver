/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package util

import (
	"fmt"
	"math"
	"net"
	"sync"
)

const (
	// Size of the network address of the IPRange we intend to reserve
	//ipRangeSize = 29
	// Maximum value of a byte
	byteMax = 255
	// Total number of bits in an IPV4 address
	ipV4Bits = 32
)

//var (
	// step size for IP range increment
	//incrementStep29IPRange = (byte)(math.Exp2(ipV4Bits - ipRangeSize))
	// mask for IP range
	//ipRangeMask = net.CIDRMask(ipRangeSize, ipV4Bits)
//)

// IPAllocator struct consists of shared resources that are used to keep track of the /29 IPRanges currently reserved by service instances
type IPAllocator struct {
	// pendingIPs set maintains the set of  IPs that have been reserved by the service instances but pending reservation in the cloud instances
	pendingIPs map[string]bool

	// pendingIPsMutex is used to synchronize access to the pendingIPRs set to prevent data races
	pendingIPsMutex sync.Mutex
}

// NewIPAllocator is the constructor to initialize the IPAllocator object
// Argument pendingIPs map[string]bool is a set of IPs currently reserved by service instances but pending reservation in the cloud instances
func NewIPAllocator(pendingIPs map[string]bool) *IPAllocator {
	// Make a copy of the pending IP ranges and set it in the IPAllocator so that the caller cannot mutate this map outside the library
	pendingIPsCopy := make(map[string]bool)
	for pendingIP := range pendingIPs {
		pendingIPsCopy[pendingIP] = true
	}
	return &IPAllocator{
		pendingIPs: pendingIPsCopy,
	}
}

// holdIPs adds a particular IP in the pendingIPs set
func (ipAllocator *IPAllocator) holdIPRange(ip string) {
	ipAllocator.pendingIPs[ip] = true
}

// ReleaseIP releases the pending IP
func (ipAllocator *IPAllocator) ReleaseIP(ip string) {
	ipAllocator.pendingIPsMutex.Lock()
	defer ipAllocator.pendingIPsMutex.Unlock()
	delete(ipAllocator.pendingIPs, ipRange)
}

// GetUnreservedIP returns an unreserved IP.
func (ipAllocator *IPAllocator) GetUnreservedIP(cidr string, cloudInstancesReservedIPs map[string]bool) (string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	var reservedIPs = make(map[string]bool)

	// The final reserved list is obtained by combining the cloudInstancesReservedIPRanges list and the pendingIPRanges list in the ipAllocator
	for cloudInstancesReservedIP := range cloudInstancesReservedIPs {
		reservedIPs[cloudInstancesReservedIP] = true
	}

	// Lock is placed here so that the pendingIPRanges list captures all the IPs pending reservation in the cloud instances
	ipAllocator.pendingIPsMutex.Lock()
	defer ipAllocator.pendingIPsMutex.Unlock()
	for reservedIP := range ipAllocator.pendingIPs {
		reservedIPs[reservedIP] = true
	}

	for targetIP := cloneIP(ip.Mask(ipnet.Mask)); ipnet.Contains(targetIP) && err == nil; targetIP, err = incrementIP(targetIP, 1) {
		used := false
		for reservedIP := range reservedIPs {
			if targetIP.Equal(reservedIP) {
				used = true
			}
		}
		if !used {
			ipAllocator.holdIP(targetIP)
			return targetIP, nil
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
