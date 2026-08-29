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

	// CapabilityMachineStart indicates support for starting a virtual machine.
	CapabilityMachineStart = "machine.start"

	// CapabilityMachineStop indicates support for stopping a virtual machine.
	CapabilityMachineStop = "machine.stop"

	// CapabilityCheckpointList indicates support for enumerating checkpoints.
	CapabilityCheckpointList = "checkpoint.list"

	// CapabilityCheckpointCreate indicates support for creating a checkpoint.
	CapabilityCheckpointCreate = "checkpoint.create"

	// CapabilityCheckpointRestore indicates support for restoring a checkpoint.
	CapabilityCheckpointRestore = "checkpoint.restore"
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

// DirectMachineCapabilities returns the full capability set supported by the direct Hyper-V recovery backend.
func DirectMachineCapabilities() CapabilitySet {
	return NewCapabilitySet(
		CapabilityHostDiagnostics,
		CapabilityMachineList,
		CapabilityMachineInspect,
		CapabilityNetworkAdapterObserve,
		CapabilityMachineStart,
		CapabilityMachineStop,
		CapabilityCheckpointList,
		CapabilityCheckpointCreate,
		CapabilityCheckpointRestore,
	)
}
