package cli

const (
	// ExitSuccess indicates normal successful completion.
	ExitSuccess = 0

	// ExitUsage indicates incorrect command invocation, missing arguments, or invalid flags.
	ExitUsage = 2

	// ExitNotFound indicates the target machine or resource was not found.
	ExitNotFound = 3

	// ExitBackendUnavailable indicates PowerShell, Hyper-V, or host management is unreachable.
	ExitBackendUnavailable = 4

	// ExitMalformedProvider indicates the provider returned invalid, corrupt, or unexpected data.
	ExitMalformedProvider = 5

	// ExitTimeout indicates the operation exceeded its allotted deadline.
	ExitTimeout = 6
)
