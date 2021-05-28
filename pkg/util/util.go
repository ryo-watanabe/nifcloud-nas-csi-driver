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

const (
	// Gb expresses GiB
	Gb = 1024 * 1024 * 1024
	// Tb expresses TiB
	Tb = 1024 * Gb
)

// RoundBytesToGb rounds up to the nearest Gb
func RoundBytesToGb(bytes int64) int64 {
	return (bytes + Gb - 1) / Gb
}

// GbToBytes converts GiB to bytes
func GbToBytes(gbs int64) int64 {
	return gbs * Gb
}

// Min chooses lesser one
func Min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// Max chooses bigger one
func Max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
