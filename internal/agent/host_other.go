//go:build !linux

package agent

func CollectHostResources() (HostResources, bool) {
	return HostResources{}, false
}
