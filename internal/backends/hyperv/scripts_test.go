package hyperv_test

import (
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
)

func TestScripts_NoMutatingVerbs(t *testing.T) {
	scripts := map[string]string{
		"ScriptDoctor":  hyperv.ScriptDoctor,
		"ScriptList":    hyperv.ScriptList,
		"ScriptInspect": hyperv.ScriptInspect,
	}

	forbiddenVerbs := []string{
		"Start-", "Stop-", "Set-", "New-", "Remove-",
		"Checkpoint-", "Restore-", "Invoke-Command", "Invoke-",
		"Restart-", "Add-", "Clear-", "Enable-", "Disable-",
		"Grant-", "Revoke-", "Rename-", "Move-", "Export-", "Import-VM",
	}

	for name, script := range scripts {
		for _, verb := range forbiddenVerbs {
			if strings.Contains(script, verb) {
				t.Errorf("script %s contains forbidden mutating verb %q", name, verb)
			}
		}
	}
}

func TestScripts_RequireErrorActionPreferenceAndModuleImport(t *testing.T) {
	scripts := map[string]string{
		"ScriptDoctor":  hyperv.ScriptDoctor,
		"ScriptList":    hyperv.ScriptList,
		"ScriptInspect": hyperv.ScriptInspect,
	}

	for name, script := range scripts {
		if !strings.Contains(script, "$ErrorActionPreference = 'Stop'") {
			t.Errorf("script %s missing $ErrorActionPreference = 'Stop'", name)
		}
		if !strings.Contains(script, "Import-Module Hyper-V -ErrorAction Stop") {
			t.Errorf("script %s missing Import-Module Hyper-V -ErrorAction Stop", name)
		}
		if strings.Contains(script, "$_.Exception.Message") {
			t.Errorf("script %s must never interpolate $_.Exception.Message", name)
		}
		if strings.Contains(script, "SilentlyContinue") {
			t.Errorf("script %s must not use SilentlyContinue", name)
		}
	}
}

func TestScripts_NetworkAdapterUsesErrorActionStop(t *testing.T) {
	scripts := map[string]string{
		"ScriptList":    hyperv.ScriptList,
		"ScriptInspect": hyperv.ScriptInspect,
	}

	for name, script := range scripts {
		if !strings.Contains(script, "Get-VMNetworkAdapter -VM $vm -ErrorAction Stop") {
			t.Errorf("script %s must query network adapters with -ErrorAction Stop", name)
		}
	}
}

func TestScripts_InspectUsesEnvironmentAndGuidParse(t *testing.T) {
	inspect := hyperv.ScriptInspect
	if !strings.Contains(inspect, "$env:AMC_TARGET_VM_ID") {
		t.Errorf("ScriptInspect must read target from $env:AMC_TARGET_VM_ID")
	}
	if !strings.Contains(inspect, "[guid]::Parse") {
		t.Errorf("ScriptInspect must parse GUID using [guid]::Parse")
	}
	if !strings.Contains(inspect, "Get-VM -Id") {
		t.Errorf("ScriptInspect must query using Get-VM -Id")
	}
}

func TestScripts_VMIdGuidProperty(t *testing.T) {
	scripts := map[string]string{
		"ScriptList":    hyperv.ScriptList,
		"ScriptInspect": hyperv.ScriptInspect,
	}

	for name, script := range scripts {
		if !strings.Contains(script, "$vm.VMId.Guid") {
			t.Errorf("script %s must use documented property $vm.VMId.Guid", name)
		}
		if strings.Contains(script, "$vm.Id.Guid") {
			t.Errorf("script %s must not use incorrect property spelling $vm.Id.Guid", name)
		}
	}
}

func TestScripts_AccessPreflightSIDsAndPrincipalChecks(t *testing.T) {
	scripts := map[string]string{
		"ScriptDoctor":  hyperv.ScriptDoctor,
		"ScriptList":    hyperv.ScriptList,
		"ScriptInspect": hyperv.ScriptInspect,
	}

	requiredSnippets := []string{
		"[System.Security.Principal.WindowsIdentity]::GetCurrent()",
		"[System.Security.Principal.WindowsPrincipal]::new($identity)",
		"[System.Security.Principal.SecurityIdentifier]::new('S-1-5-32-544')",
		"[System.Security.Principal.SecurityIdentifier]::new('S-1-5-32-578')",
		"$principal.IsInRole($adminSid)",
		"$principal.IsInRole($hypervAdminSid)",
		`error_category = "access_denied"`,
	}

	for name, script := range scripts {
		for _, snippet := range requiredSnippets {
			if !strings.Contains(script, snippet) {
				t.Errorf("script %s missing required preflight snippet %q", name, snippet)
			}
		}

		if strings.Contains(script, "whoami") {
			t.Errorf("script %s must not invoke whoami", name)
		}
		if strings.Contains(script, "net localgroup") {
			t.Errorf("script %s must not invoke net localgroup", name)
		}
	}
}

func assertOrder(t *testing.T, script, first, second string) {
	t.Helper()
	idx1 := strings.Index(script, first)
	idx2 := strings.Index(script, second)
	if idx1 == -1 {
		t.Fatalf("missing expected pattern %q", first)
	}
	if idx2 == -1 {
		t.Fatalf("missing expected pattern %q", second)
	}
	if idx1 >= idx2 {
		t.Errorf("expected %q (%d) to appear before %q (%d)", first, idx1, second, idx2)
	}
}

func TestScripts_PreflightOrdering(t *testing.T) {
	t.Run("ScriptDoctor", func(t *testing.T) {
		assertOrder(t, hyperv.ScriptDoctor, "Import-Module Hyper-V -ErrorAction Stop", "[System.Security.Principal.WindowsIdentity]::GetCurrent()")
		assertOrder(t, hyperv.ScriptDoctor, "$principal.IsInRole($adminSid)", "Get-VMHost")
	})

	t.Run("ScriptList", func(t *testing.T) {
		assertOrder(t, hyperv.ScriptList, "Import-Module Hyper-V -ErrorAction Stop", "[System.Security.Principal.WindowsIdentity]::GetCurrent()")
		assertOrder(t, hyperv.ScriptList, "$principal.IsInRole($adminSid)", "Get-VM -ErrorAction Stop")
		assertOrder(t, hyperv.ScriptList, "Get-VM -ErrorAction Stop", "Get-VMNetworkAdapter -VM $vm -ErrorAction Stop")
	})

	t.Run("ScriptInspect", func(t *testing.T) {
		assertOrder(t, hyperv.ScriptInspect, "$env:AMC_TARGET_VM_ID", "Import-Module Hyper-V -ErrorAction Stop")
		assertOrder(t, hyperv.ScriptInspect, "Import-Module Hyper-V -ErrorAction Stop", "[System.Security.Principal.WindowsIdentity]::GetCurrent()")
		assertOrder(t, hyperv.ScriptInspect, "$principal.IsInRole($adminSid)", "Get-VM -Id $vmGuid -ErrorAction Stop")
		assertOrder(t, hyperv.ScriptInspect, "Get-VM -Id $vmGuid -ErrorAction Stop", "Get-VMNetworkAdapter -VM $vm -ErrorAction Stop")
	})
}
