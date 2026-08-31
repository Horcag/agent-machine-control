//go:build unix

package target

func mutationJournalNewDirectorySecurityCalls() string { return "protect-new,validate" }

func mutationJournalNewDirectoryProtectFailureCalls() string { return "protect-new" }

func mutationJournalUsesPostCreateProtection() bool { return true }
