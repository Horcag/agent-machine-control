package domain

const (
	// CapabilityHostDiagnostics indicates support for diagnosing hypervisor and host health.
	CapabilityHostDiagnostics = "host.diagnostics"

	// CapabilityMachineList indicates support for enumerating virtual machines.
	CapabilityMachineList = "machine.list"

	// CapabilityMachineInspect indicates support for inspecting virtual machine configuration and state.
	CapabilityMachineInspect = "machine.inspect"

	// CapabilityNetworkAdapterObserve indicates support for observing VM network adapter state.
	CapabilityNetworkAdapterObserve = "network_adapter.observe"
)

// ReadOnlyMachineCapabilities returns the capability set for read-only Hyper-V observation.
// It explicitly contains no mutating, console input, PTY, or sidecar capabilities.
func ReadOnlyMachineCapabilities() CapabilitySet {
	return NewCapabilitySet(
		CapabilityHostDiagnostics,
		CapabilityMachineList,
		CapabilityMachineInspect,
		CapabilityNetworkAdapterObserve,
	)
}
