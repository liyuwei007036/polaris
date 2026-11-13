//go:build !linux

package agent

func CollectHostResources() (HostResources, bool) {
	return HostResources{}, false
}

func readHostMemTotal() uint64 {
	return 0
}

