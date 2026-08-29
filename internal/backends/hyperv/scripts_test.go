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
