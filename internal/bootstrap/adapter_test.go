package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func TestPowerShellAdapterIdentityDesiredAndLifecycleActions(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(t.TempDir(), "amcd")
	if err := os.WriteFile(binary, []byte("synthetic-amcd"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{host: hostContext{
		Account: `SYNTHETIC\operator`, SID: "S-1-5-21-1000",
		LocalAppData: `C:\Users\operator\AppData\Local`, SystemRoot: `C:\Windows`,
		WSLExecutable: `C:\Windows\System32\wsl.exe`,
	}}
	adapter := &PowerShellAdapter{
		runner: runner,
		getenv: func(name string) string {
			if name == "WSL_DISTRO_NAME" {
				return "Synthetic-WSL"
			}
			return ""
		},
		executable:  func() (string, error) { return binary, nil },
		currentUser: func() (*user.User, error) { return &user.User{Username: "operator"}, nil },
		wslRuntime:  func() bool { return true },
	}
	identity, err := adapter.Identity(context.Background())
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}
	spec, err := adapter.Desired(context.Background(), t.TempDir(), identity)
	if err != nil {
		t.Fatalf("Desired() error = %v", err)
	}
	observation, err := adapter.Inspect(context.Background(), spec)
	if err != nil || observation.State != app.BootstrapStopped {
		t.Fatalf("Inspect() = %#v, %v", observation, err)
	}
	for _, call := range []struct {
		name string
		fn   func(context.Context, app.BootstrapSpec) error
	}{
		{"install", adapter.Install}, {"start", adapter.StartTask},
		{"stop", adapter.StopTask}, {"remove", adapter.Remove},
	} {
		if err := call.fn(context.Background(), spec); err != nil {
			t.Fatalf("%s() error = %v", call.name, err)
		}
	}
	wantActions := []string{"inspect", "install", "start", "stop", "remove"}
	if strings.Join(runner.actions, ",") != strings.Join(wantActions, ",") {
		t.Fatalf("scheduler actions = %v, want %v", runner.actions, wantActions)
	}
}

func TestPowerShellAdapterDesiredUsesEnvironmentDistroBeforeDefault(t *testing.T) {
	t.Parallel()

	adapter := newTestPowerShellAdapter(t, "Environment-WSL", "Registry-WSL")
	identity, err := adapter.Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	spec, err := adapter.Desired(t.Context(), t.TempDir(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Distro != "Environment-WSL" {
		t.Fatalf("distro = %q, want environment distro", spec.Distro)
	}
}

func TestPowerShellAdapterDesiredUsesTrustedDefaultDistroWhenEnvironmentIsStripped(t *testing.T) {
	t.Parallel()

	adapter := newTestPowerShellAdapter(t, "", "Ubuntu-24.04")
	identity, err := adapter.Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	spec, err := adapter.Desired(t.Context(), t.TempDir(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Distro != "Ubuntu-24.04" {
		t.Fatalf("distro = %q, want trusted default", spec.Distro)
	}
}

func TestPowerShellAdapterDesiredRejectsMissingOrMalformedDefaultDistro(t *testing.T) {
	t.Parallel()

	for _, distro := range []string{"", "Ubuntu 24.04"} {
		t.Run(distro, func(t *testing.T) {
			adapter := newTestPowerShellAdapter(t, "", distro)
			identity, err := adapter.Identity(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.Desired(t.Context(), t.TempDir(), identity); !errors.Is(err, app.ErrBootstrapUnsupported) {
				t.Fatalf("Desired() error = %v, want unsupported", err)
			}
		})
	}
}

func TestPowerShellAdapterFailsClosedOnUnsupportedOrMalformedHost(t *testing.T) {
	t.Parallel()

	adapter := &PowerShellAdapter{
		runner:     &fakeCommandRunner{raw: []byte(`{"account":"only"}`)},
		getenv:     func(string) string { return "" },
		wslRuntime: func() bool { return false },
	}
	if _, err := adapter.Desired(context.Background(), "/state", app.BootstrapIdentity{}); !errors.Is(err, app.ErrBootstrapUnsupported) {
		t.Fatalf("Desired() error = %v, want unsupported", err)
	}
	if _, err := adapter.Identity(context.Background()); err == nil {
		t.Fatal("Identity() accepted an incomplete host identity")
	}
	if err := decodeSingleJSON([]byte(`{} {}`), &hostContext{}); err == nil {
		t.Fatal("decodeSingleJSON() accepted trailing JSON")
	}
}

func TestHostContextScriptReadsOneCurrentUserDefaultDistro(t *testing.T) {
	t.Parallel()

	for _, required := range []string{
		`HKCU:\Software\Microsoft\Windows\CurrentVersion\Lxss`,
		"DefaultDistribution",
		"PSChildName",
		"DistributionName",
		"$matches.Count -eq 1",
		"default_distro = $defaultDistro",
	} {
		if !strings.Contains(hostContextScript, required) {
			t.Errorf("host context script missing %q", required)
		}
	}
}

func TestPowerShellAdapterPropagatesDependencyAndSchedulerFailures(t *testing.T) {
	t.Parallel()

	host := hostContext{
		Account: `SYNTHETIC\operator`, SID: "S-1-5-21-1000",
		LocalAppData: `C:\Users\operator\AppData\Local`, SystemRoot: `C:\Windows`,
		WSLExecutable: `C:\Windows\System32\wsl.exe`,
	}
	identity := app.BootstrapIdentity{Account: host.Account, SID: host.SID}
	base := &PowerShellAdapter{
		runner:      &fakeCommandRunner{host: host},
		getenv:      func(string) string { return "Synthetic-WSL" },
		currentUser: func() (*user.User, error) { return nil, errors.New("user lookup failed") },
		executable:  func() (string, error) { return "", errors.New("executable lookup failed") },
		wslRuntime:  func() bool { return true },
	}
	if _, err := base.Desired(context.Background(), "/state", identity); err == nil {
		t.Fatal("Desired() ignored current-user lookup failure")
	}
	base.currentUser = func() (*user.User, error) { return &user.User{Username: "operator"}, nil }
	if _, err := base.Desired(context.Background(), "/state", identity); err == nil {
		t.Fatal("Desired() ignored executable lookup failure")
	}
	base.runner = failingCommandRunner{}
	if _, err := base.Identity(context.Background()); err == nil {
		t.Fatal("Identity() ignored PowerShell failure")
	}

	spec := app.BootstrapSpec{WSLExecutable: `C:\Windows\System32\wsl.exe`, Distro: "Synthetic-WSL", LinuxUser: "operator", BinaryPath: "/bin/amcd", StateDir: "/state", ListenAddress: "127.0.0.1:0"}
	base.runner = &fakeCommandRunner{host: host, schedulerRaw: []byte(`not-json`)}
	if _, err := base.Inspect(context.Background(), spec); err == nil {
		t.Fatal("Inspect() accepted malformed scheduler output")
	}
	production := NewPowerShellAdapter()
	if production.runner == nil || production.getenv == nil || production.executable == nil || production.currentUser == nil {
		t.Fatal("NewPowerShellAdapter() did not initialize production dependencies")
	}
}

func TestShellRunnerUsesBoundedPowerShellInvocationAndSanitizesFailures(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotArgs, gotEnv []string
	runner := shellRunner{
		lookPath: func(name string) (string, error) {
			if name != "powershell.exe" {
				t.Fatalf("lookPath(%q)", name)
			}
			return "/synthetic/powershell.exe", nil
		},
		run: func(_ context.Context, path string, args, env []string) ([]byte, error) {
			gotPath, gotArgs, gotEnv = path, args, env
			return []byte(`{"state":"stopped"}`), nil
		},
	}
	out, err := runner.Run(context.Background(), "synthetic-script", map[string]string{"AMC_TEST_INPUT": "value"})
	if err != nil || !strings.Contains(string(out), "stopped") {
		t.Fatalf("Run() = %s, %v", out, err)
	}
	if gotPath != "/synthetic/powershell.exe" || gotArgs[len(gotArgs)-1] != "synthetic-script" {
		t.Fatalf("command path=%q args=%v", gotPath, gotArgs)
	}
	if !containsString(gotEnv, "AMC_TEST_INPUT=value") {
		t.Fatalf("environment did not contain encoded input")
	}

	runner.run = func(context.Context, string, []string, []string) ([]byte, error) {
		return []byte("private host details"), errors.New("exit 1")
	}
	if _, err := runner.Run(context.Background(), "script", nil); err == nil || strings.Contains(err.Error(), "private host details") {
		t.Fatalf("Run() leaked subprocess output: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Run(cancelled, "script", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
	runner.lookPath = func(string) (string, error) { return "", errors.New("missing") }
	if _, err := runner.Run(context.Background(), "script", nil); !errors.Is(err, app.ErrBootstrapUnsupported) {
		t.Fatalf("Run() error = %v, want unsupported", err)
	}
}

func TestShellRunnerForwardsOnlyFixedBootstrapPayloadThroughWSLEnv(t *testing.T) {
	t.Parallel()

	var gotEnv []string
	runner := shellRunner{
		lookPath: func(string) (string, error) { return "/synthetic/powershell.exe", nil },
		environ: func() []string {
			return []string{"PATH=/usr/bin", "WSLENV=EXISTING/u:AMC_BOOTSTRAP_ACTION/p:EXISTING/w"}
		},
		wslRuntime: func() bool { return true },
		run: func(_ context.Context, _ string, _ []string, env []string) ([]byte, error) {
			gotEnv = env
			return []byte(`{"state":"stopped"}`), nil
		},
	}

	_, err := runner.Run(t.Context(), "synthetic-script", map[string]string{
		"AMC_BOOTSTRAP_ACTION":       "inspect",
		"AMC_BOOTSTRAP_SPEC_B64":     "encoded-spec",
		"AMC_BOOTSTRAP_WRAPPER_B64":  "encoded-wrapper",
		"AMC_BOOTSTRAP_METADATA_B64": "encoded-metadata",
		"AMC_UNRELATED_INPUT":        "must-not-cross-interop",
	})
	if err != nil {
		t.Fatal(err)
	}
	const wantWSLEnv = "EXISTING/u:AMC_BOOTSTRAP_ACTION/p:AMC_BOOTSTRAP_SPEC_B64:AMC_BOOTSTRAP_WRAPPER_B64:AMC_BOOTSTRAP_METADATA_B64"
	if value := testWSLEnvValue(gotEnv); value != wantWSLEnv {
		t.Fatalf("WSLENV = %q, want %q", value, wantWSLEnv)
	}
	if strings.Contains(testWSLEnvValue(gotEnv), "AMC_UNRELATED_INPUT") {
		t.Fatalf("WSLENV forwarded unrelated input: %q", testWSLEnvValue(gotEnv))
	}
}

func TestShellRunnerDoesNotSynthesizeWSLEnvOutsideWSL(t *testing.T) {
	t.Parallel()

	var gotEnv []string
	runner := shellRunner{
		lookPath:   func(string) (string, error) { return "/synthetic/powershell.exe", nil },
		environ:    func() []string { return []string{"PATH=/usr/bin"} },
		wslRuntime: func() bool { return false },
		run: func(_ context.Context, _ string, _ []string, env []string) ([]byte, error) {
			gotEnv = env
			return []byte(`{"state":"stopped"}`), nil
		},
	}

	_, err := runner.Run(t.Context(), "synthetic-script", map[string]string{"AMC_BOOTSTRAP_ACTION": "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	if value := testWSLEnvValue(gotEnv); value != "" {
		t.Fatalf("WSLENV = %q, want no synthesized value outside WSL", value)
	}
}

func TestBuildSpecProducesExactS4ULimitedFingerprint(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(t.TempDir(), "amcd")
	if err := os.WriteFile(binary, []byte("synthetic-amcd"), 0600); err != nil {
		t.Fatal(err)
	}
	identity := app.BootstrapIdentity{Account: `SYNTHETIC\operator`, SID: "S-1-5-21-1000"}
	spec, err := buildSpec(hostContext{
		Account: identity.Account, SID: identity.SID,
		LocalAppData: `C:\Users\operator\AppData\Local`, SystemRoot: `C:\Windows`,
		WSLExecutable: `C:\Windows\System32\wsl.exe`,
	}, identity, "Synthetic-WSL", "operator", t.TempDir(), binary)
	if err != nil {
		t.Fatalf("buildSpec() error = %v", err)
	}
	if spec.LogonType != "S4U" || spec.RunLevel != "Limited" || !spec.LogonTrigger || !spec.StartWhenAvailable {
		t.Fatalf("unsafe principal/trigger/settings: %#v", spec)
	}
	if spec.MultipleInstances != "IgnoreNew" || spec.ExecutionTimeLimit != "PT0S" {
		t.Fatalf("unsafe lifecycle settings: %#v", spec)
	}
	if err := spec.Validate(identity); err != nil {
		t.Fatalf("spec.Validate() error = %v", err)
	}
	if spec.WrapperSHA256 == "" || spec.MetadataSHA256 == "" || spec.BinarySHA256 == "" {
		t.Fatalf("missing fingerprints: %#v", spec)
	}
	assertPowerShellFileAction(t, spec)
}

func assertPowerShellFileAction(t *testing.T, spec app.BootstrapSpec) {
	t.Helper()

	if !strings.Contains(spec.ActionExecutable, "WindowsPowerShell\\v1.0\\powershell.exe") {
		t.Fatalf("action executable = %q, want canonical Windows PowerShell", spec.ActionExecutable)
	}
	wantArguments := `-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "` + spec.WrapperPath + `"`
	if spec.ActionArguments != wantArguments {
		t.Fatalf("action arguments = %q, want %q", spec.ActionArguments, wantArguments)
	}
	if !strings.HasSuffix(spec.WrapperPath, ".ps1") || strings.Contains(spec.ActionArguments, "-Command") {
		t.Fatalf("unsafe PowerShell file action: %#v", spec)
	}
}

func TestWrapperContainsOnlyFixedDaemonBootstrapInputs(t *testing.T) {
	t.Parallel()

	spec := app.BootstrapSpec{
		WSLExecutable: `C:\Windows\System32\wsl.exe`, Distro: "Synthetic-WSL", LinuxUser: "operator",
		BinaryPath: "/usr/local/bin/amcd", StateDir: "/mnt/c/ProgramData/amc", ListenAddress: "127.0.0.1:0",
	}
	wrapper, err := wrapperBytes(spec)
	if err != nil {
		t.Fatalf("wrapperBytes() error = %v", err)
	}
	text := string(wrapper)
	for _, required := range []string{
		"wsl.exe", "Synthetic-WSL", "operator", "/usr/local/bin/amcd", "run", "--state-dir", "127.0.0.1:0",
		`'Synthetic-WSL'`, `'operator'`, `'/usr/local/bin/amcd'`, `'127.0.0.1:0'`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("wrapper missing %q: %s", required, text)
		}
	}
	for _, forbidden := range []string{"token", "password", "vm_guid", "machine_id", "transcript", "--direct"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Errorf("wrapper contains forbidden %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{
		"Start-Process", "-ArgumentList $arguments", "-NoNewWindow", "-Wait", "-PassThru", "exit $child.ExitCode",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("PowerShell launcher missing %q: %s", required, text)
		}
	}
}

func TestWrapperPreservesSimpleNativeArgumentsAndQuotesWhitespaceArguments(t *testing.T) {
	t.Parallel()

	spec := app.BootstrapSpec{
		WSLExecutable: `C:\Windows\System32\wsl.exe`, Distro: "Ubuntu-24.04", LinuxUser: "operator",
		BinaryPath: "/mnt/c/Example User/bin/amcd", StateDir: "/mnt/c/Example User/amc state", ListenAddress: "127.0.0.1:0",
	}
	wrapper, err := wrapperBytes(spec)
	if err != nil {
		t.Fatalf("wrapperBytes() error = %v", err)
	}
	text := string(wrapper)
	for _, required := range []string{
		`'Ubuntu-24.04'`,
		`'operator'`,
		`'"/mnt/c/Example User/bin/amcd"'`,
		`'"/mnt/c/Example User/amc state"'`,
		`'127.0.0.1:0'`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("wrapper missing quoted native argument %q: %s", required, text)
		}
	}
	for _, unexpected := range []string{`'"Ubuntu-24.04"'`, `'"operator"'`, `'"127.0.0.1:0"'`} {
		if strings.Contains(text, unexpected) {
			t.Errorf("wrapper rendered safe native argument with literal quotes %q: %s", unexpected, text)
		}
	}
}

func TestQuoteWindowsArgument(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, value, want string
	}{
		{name: "simple", value: "Ubuntu-24.04", want: "Ubuntu-24.04"},
		{name: "empty", value: "", want: `""`},
		{name: "space", value: "/mnt/c/Example User/amc", want: `"/mnt/c/Example User/amc"`},
		{name: "tab", value: "/mnt/c/Example\tUser/amc", want: "\"/mnt/c/Example\tUser/amc\""},
		{name: "quote", value: `C:\\Example"User`, want: `"C:\\Example\"User"`},
		{name: "one trailing backslash", value: `/mnt/c/Example User/amc\`, want: `"/mnt/c/Example User/amc\\"`},
		{name: "two trailing backslashes", value: `/mnt/c/Example User/amc\\`, want: `"/mnt/c/Example User/amc\\\\"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := quoteWindowsArgument(test.value); got != test.want {
				t.Errorf("quoteWindowsArgument(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestWrapperRejectsPowerShellEscapes(t *testing.T) {
	t.Parallel()

	base := app.BootstrapSpec{
		WSLExecutable: "wsl.exe", Distro: "Synthetic-WSL", LinuxUser: "operator",
		BinaryPath: "/usr/local/bin/amcd", StateDir: "/state", ListenAddress: "127.0.0.1:0",
	}
	for name, mutate := range map[string]func(*app.BootstrapSpec){
		"single quote":             func(spec *app.BootstrapSpec) { spec.StateDir = "/state'" },
		"double quote":             func(spec *app.BootstrapSpec) { spec.StateDir = `/state"` },
		"carriage return":          func(spec *app.BootstrapSpec) { spec.BinaryPath = "/bin/amcd" + string([]byte{'\r'}) },
		"line feed":                func(spec *app.BootstrapSpec) { spec.BinaryPath = "/bin/amcd" + string([]byte{'\n'}) },
		"PowerShell variable":      func(spec *app.BootstrapSpec) { spec.StateDir = "/state$env:Path" },
		"PowerShell escape":        func(spec *app.BootstrapSpec) { spec.StateDir = "/state`n" },
		"PowerShell subexpression": func(spec *app.BootstrapSpec) { spec.StateDir = "/state$(Get-Date)" },
	} {
		t.Run(name, func(t *testing.T) {
			spec := base
			mutate(&spec)
			if _, err := wrapperBytes(spec); err == nil {
				t.Fatal("wrapperBytes() accepted unsafe PowerShell text")
			}
		})
	}
}

func TestWrapperRejectsShellMetacharacters(t *testing.T) {
	t.Parallel()

	_, err := wrapperBytes(app.BootstrapSpec{
		WSLExecutable: `C:\Windows\System32\wsl.exe`, Distro: "Synthetic&calc", LinuxUser: "operator",
		BinaryPath: "/usr/local/bin/amcd", StateDir: "/state", ListenAddress: "127.0.0.1:0",
	})
	if err == nil {
		t.Fatal("wrapperBytes() accepted a command metacharacter")
	}
}

func TestTaskSchedulerScriptPinsOwnedTaskSecurityContract(t *testing.T) {
	t.Parallel()

	for _, required := range []string{
		"WindowsIdentity]::GetCurrent", "-LogonType S4U", "-RunLevel Limited", "New-ScheduledTaskTrigger -AtLogOn",
		"-StartWhenAvailable", "-MultipleInstances IgnoreNew", "Test-PrivateAcl", "Test-Hash", "Unregister-ScheduledTask",
		"AreAccessRulesCanonical", "FileSystemRights]::FullControl", "Test-OwnedTaskFingerprint", "AllowDemandStart",
		"UseUnifiedSchedulingEngine", "Export-ScheduledTask", "Get-PersistedPrincipalSid", "Test-EmptyTaskRepetition",
	} {
		if !strings.Contains(taskSchedulerScript, required) {
			t.Errorf("task scheduler script missing %q", required)
		}
	}
}

type fakeCommandRunner struct {
	host         hostContext
	raw          []byte
	schedulerRaw []byte
	actions      []string
}

func newTestPowerShellAdapter(t *testing.T, environmentDistro, defaultDistro string) *PowerShellAdapter {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "amcd")
	if err := os.WriteFile(binary, []byte("synthetic-amcd"), 0600); err != nil {
		t.Fatal(err)
	}
	host := hostContext{
		Account: `SYNTHETIC\operator`, SID: "S-1-5-21-1000",
		LocalAppData: `C:\Users\operator\AppData\Local`, SystemRoot: `C:\Windows`,
		WSLExecutable: `C:\Windows\System32\wsl.exe`,
		DefaultDistro: defaultDistro,
	}
	return &PowerShellAdapter{
		runner: &fakeCommandRunner{host: host},
		getenv: func(name string) string {
			if name == "WSL_DISTRO_NAME" {
				return environmentDistro
			}
			return ""
		},
		executable:  func() (string, error) { return binary, nil },
		currentUser: func() (*user.User, error) { return &user.User{Username: "operator"}, nil },
		wslRuntime:  func() bool { return true },
	}
}

func containsString(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}

func testWSLEnvValue(env []string) string {
	for _, entry := range slices.Backward(env) {
		if value, ok := strings.CutPrefix(entry, "WSLENV="); ok {
			return value
		}
	}
	return ""
}

func (f *fakeCommandRunner) Run(_ context.Context, script string, env map[string]string) ([]byte, error) {
	if script == hostContextScript {
		if f.raw != nil {
			return f.raw, nil
		}
		return json.Marshal(f.host)
	}
	action := env["AMC_BOOTSTRAP_ACTION"]
	f.actions = append(f.actions, action)
	if f.schedulerRaw != nil {
		return f.schedulerRaw, nil
	}
	return json.Marshal(app.BootstrapObservation{
		State: app.BootstrapStopped, Reason: app.BootstrapReasonStopped, Exact: true,
	})
}

type failingCommandRunner struct{}

func (failingCommandRunner) Run(context.Context, string, map[string]string) ([]byte, error) {
	return nil, errors.New("synthetic command failure")
}
