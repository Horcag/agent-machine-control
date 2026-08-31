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

	// CapabilitySessionSSH indicates support for establishing persistent SSH sessions.
	CapabilitySessionSSH = "session.ssh"

	// CapabilitySessionPTY indicates support for interactive pseudo-terminal allocation.
	CapabilitySessionPTY = "session.pty"

	// CapabilitySessionOpen indicates support for establishing persistent SSH/PTY sessions.
	CapabilitySessionOpen = "session.open"

	// CapabilitySessionRead indicates support for non-blocking incremental stream reads.
	CapabilitySessionRead = "session.read"

	// CapabilitySessionWrite indicates support for writing character data to session stdin.
	CapabilitySessionWrite = "session.write"

	// CapabilitySessionControl indicates support for sending terminal control keys.
	CapabilitySessionControl = "session.control"

	// CapabilitySessionWait indicates support for settle-time and regex stream waits.
	CapabilitySessionWait = "session.wait"

	// CapabilitySessionList indicates support for enumerating active terminal sessions.
	CapabilitySessionList = "session.list"

	// CapabilitySessionShow indicates support for inspecting terminal session state and metrics.
	CapabilitySessionShow = "session.show"

	// CapabilitySessionClose indicates support for gracefully closing a terminal session.
	CapabilitySessionClose = "session.close"

	// CapabilitySessionAttach indicates support for interactive TTY attach/detach.
	CapabilitySessionAttach = "session.attach"
)

// SessionCapabilities returns the capability set for persistent SSH/PTY sessions.
func SessionCapabilities() CapabilitySet {
	return NewCapabilitySet(
		CapabilitySessionSSH,
		CapabilitySessionPTY,
		CapabilitySessionOpen,
		CapabilitySessionRead,
		CapabilitySessionWrite,
		CapabilitySessionControl,
		CapabilitySessionWait,
		CapabilitySessionList,
		CapabilitySessionShow,
		CapabilitySessionClose,
		CapabilitySessionAttach,
	)
}

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
