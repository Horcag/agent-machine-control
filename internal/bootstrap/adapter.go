package bootstrap

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/wslruntime"
)

const (
	taskPath = `\AgentMachineControl\`
	taskName = "amcd-current-user"
)

var bootstrapPayloadEnvironmentNames = [...]string{
	"AMC_BOOTSTRAP_ACTION",
	"AMC_BOOTSTRAP_SPEC_B64",
	"AMC_BOOTSTRAP_WRAPPER_B64",
	"AMC_BOOTSTRAP_METADATA_B64",
}

//go:embed task_fingerprint.ps1
var taskFingerprintScript string

//go:embed task_scheduler.ps1
var taskSchedulerEntryScript string

var taskSchedulerScript = taskFingerprintScript + "\n" + taskSchedulerEntryScript

type commandRunner interface {
	Run(context.Context, string, map[string]string) ([]byte, error)
}

type PowerShellAdapter struct {
	runner      commandRunner
	getenv      func(string) string
	executable  func() (string, error)
	currentUser func() (*user.User, error)
	wslRuntime  func() bool
}

func NewPowerShellAdapter() *PowerShellAdapter {
	return &PowerShellAdapter{
		runner: shellRunner{}, getenv: os.Getenv,
		executable: os.Executable, currentUser: user.Current,
	}
}

type hostContext struct {
	Account       string `json:"account"`
	SID           string `json:"sid"`
	LocalAppData  string `json:"local_app_data"`
	SystemRoot    string `json:"system_root"`
	WSLExecutable string `json:"wsl_executable"`
	DefaultDistro string `json:"default_distro"`
}

func (a *PowerShellAdapter) Identity(ctx context.Context) (app.BootstrapIdentity, error) {
	host, err := a.readHostContext(ctx)
	if err != nil {
		return app.BootstrapIdentity{}, err
	}
	identity := app.BootstrapIdentity{Account: host.Account, SID: host.SID}
	if err := identity.Validate(); err != nil {
		return app.BootstrapIdentity{}, err
	}
	return identity, nil
}

func (a *PowerShellAdapter) Desired(ctx context.Context, stateDir string, identity app.BootstrapIdentity) (app.BootstrapSpec, error) {
	if !a.isWSL() {
		return app.BootstrapSpec{}, app.ErrBootstrapUnsupported
	}
	host, err := a.readHostContext(ctx)
	if err != nil {
		return app.BootstrapSpec{}, err
	}
	if host.Account != identity.Account || host.SID != identity.SID {
		return app.BootstrapSpec{}, fmt.Errorf("%w: Windows identity changed during bootstrap", app.ErrBootstrapDrift)
	}
	distro := strings.TrimSpace(a.getenv("WSL_DISTRO_NAME"))
	if distro == "" {
		distro = strings.TrimSpace(host.DefaultDistro)
	}
	currentUser, err := a.currentUser()
	if err != nil {
		return app.BootstrapSpec{}, fmt.Errorf("%w: current Linux user unavailable", app.ErrBootstrapUnsupported)
	}
	binaryPath, err := a.executable()
	if err != nil {
		return app.BootstrapSpec{}, fmt.Errorf("%w: amcd executable path unavailable", app.ErrBootstrapUnsupported)
	}
	return buildSpec(host, identity, distro, currentUser.Username, stateDir, binaryPath)
}

func (a *PowerShellAdapter) Inspect(ctx context.Context, spec app.BootstrapSpec) (app.BootstrapObservation, error) {
	return a.invoke(ctx, "inspect", spec)
}

func (a *PowerShellAdapter) Install(ctx context.Context, spec app.BootstrapSpec) error {
	_, err := a.invoke(ctx, "install", spec)
	return err
}

func (a *PowerShellAdapter) StartTask(ctx context.Context, spec app.BootstrapSpec) error {
	_, err := a.invoke(ctx, "start", spec)
	return err
}

func (a *PowerShellAdapter) StopTask(ctx context.Context, spec app.BootstrapSpec) error {
	_, err := a.invoke(ctx, "stop", spec)
	return err
}

func (a *PowerShellAdapter) Remove(ctx context.Context, spec app.BootstrapSpec) error {
	_, err := a.invoke(ctx, "remove", spec)
	return err
}

func (a *PowerShellAdapter) readHostContext(ctx context.Context) (hostContext, error) {
	if !a.isWSL() {
		return hostContext{}, app.ErrBootstrapUnsupported
	}
	out, err := a.runner.Run(ctx, hostContextScript, nil)
	if err != nil {
		return hostContext{}, err
	}
	var host hostContext
	if err := decodeSingleJSON(out, &host); err != nil {
		return hostContext{}, fmt.Errorf("bootstrap: invalid Windows identity response: %w", err)
	}
	for _, value := range []string{host.Account, host.SID, host.LocalAppData, host.SystemRoot, host.WSLExecutable} {
		if strings.TrimSpace(value) == "" {
			return hostContext{}, fmt.Errorf("%w: incomplete Windows host identity", app.ErrBootstrapUnsupported)
		}
	}
	return host, nil
}

func (a *PowerShellAdapter) isWSL() bool {
	if a.wslRuntime != nil {
		return a.wslRuntime()
	}
	return wslruntime.IsWSL()
}

func (a *PowerShellAdapter) invoke(ctx context.Context, action string, spec app.BootstrapSpec) (app.BootstrapObservation, error) {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return app.BootstrapObservation{}, err
	}
	wrapper, err := wrapperBytes(spec)
	if err != nil {
		return app.BootstrapObservation{}, err
	}
	metadata, err := metadataBytes(spec)
	if err != nil {
		return app.BootstrapObservation{}, err
	}
	env := map[string]string{
		"AMC_BOOTSTRAP_ACTION":       action,
		"AMC_BOOTSTRAP_SPEC_B64":     base64.StdEncoding.EncodeToString(specJSON),
		"AMC_BOOTSTRAP_WRAPPER_B64":  base64.StdEncoding.EncodeToString(wrapper),
		"AMC_BOOTSTRAP_METADATA_B64": base64.StdEncoding.EncodeToString(metadata),
	}
	out, err := a.runner.Run(ctx, taskSchedulerScript, env)
	if err != nil {
		return app.BootstrapObservation{}, err
	}
	var observation app.BootstrapObservation
	if err := decodeSingleJSON(out, &observation); err != nil {
		return app.BootstrapObservation{}, fmt.Errorf("bootstrap: invalid scheduler response: %w", err)
	}
	return observation, nil
}

func decodeSingleJSON(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(string(data))))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

type shellRunner struct {
	lookPath   func(string) (string, error)
	run        func(context.Context, string, []string, []string) ([]byte, error)
	environ    func() []string
	wslRuntime func() bool
}

func (s shellRunner) Run(ctx context.Context, script string, extraEnv map[string]string) ([]byte, error) {
	lookPath := s.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := lookPath("powershell.exe")
	if err != nil {
		return nil, app.ErrBootstrapUnsupported
	}
	environ := s.environ
	if environ == nil {
		environ = os.Environ
	}
	env := environ()
	for key, value := range extraEnv {
		env = append(env, key+"="+value)
	}
	if s.runningUnderWSL() {
		if names := bootstrapPayloadNames(extraEnv); len(names) > 0 {
			env = wslruntime.ForwardNamesViaWSLEnv(env, names)
		}
	}
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
	run := s.run
	if run == nil {
		run = runPowerShellCommand
	}
	out, err := run(ctx, path, args, env)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("bootstrap: PowerShell scheduler operation failed")
	}
	return out, nil
}

func (s shellRunner) runningUnderWSL() bool {
	if s.wslRuntime != nil {
		return s.wslRuntime()
	}
	return wslruntime.IsWSL()
}

func bootstrapPayloadNames(env map[string]string) []string {
	names := make([]string, 0, len(bootstrapPayloadEnvironmentNames))
	for _, name := range bootstrapPayloadEnvironmentNames {
		if _, present := env[name]; present {
			names = append(names, name)
		}
	}
	return names
}

func runPowerShellCommand(ctx context.Context, path string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	return cmd.CombinedOutput()
}

const hostContextScript = `$ErrorActionPreference = 'Stop'
$identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
$root = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
$defaultDistro = $null
try {
  $lxssPath = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Lxss'
  $defaultDistribution = (Get-ItemProperty -LiteralPath $lxssPath -Name DefaultDistribution -ErrorAction Stop).DefaultDistribution
  $matches = @(Get-ChildItem -LiteralPath $lxssPath -ErrorAction Stop | ForEach-Object {
    $distribution = Get-ItemProperty -LiteralPath $_.PSPath -ErrorAction Stop
    if ([string]$_.PSChildName -eq [string]$defaultDistribution) {
      [string]$distribution.DistributionName
    }
  } | Where-Object { $_ })
  if ($matches.Count -eq 1) {
    $defaultDistro = $matches[0]
  }
} catch {}
[pscustomobject]@{
  account = $identity.Name
  sid = $identity.User.Value
  local_app_data = $root
  system_root = $env:SystemRoot
  wsl_executable = [IO.Path]::Combine($env:SystemRoot, 'System32', 'wsl.exe')
  default_distro = $defaultDistro
} | ConvertTo-Json -Compress
`

func buildSpec(host hostContext, identity app.BootstrapIdentity, distro, linuxUser, stateDir, binaryPath string) (app.BootstrapSpec, error) {
	if err := validateSimpleName("distro", distro); err != nil {
		return app.BootstrapSpec{}, err
	}
	if err := validateSimpleName("Linux user", linuxUser); err != nil {
		return app.BootstrapSpec{}, err
	}
	resolvedState, err := statedir.Resolve(stateDir)
	if err != nil {
		return app.BootstrapSpec{}, err
	}
	stateRoot := resolvedState.Root()
	for label, value := range map[string]string{"state directory": stateRoot, "binary path": binaryPath} {
		if !filepath.IsAbs(value) || hasPowerShellMetacharacter(value) {
			return app.BootstrapSpec{}, fmt.Errorf("%w: unsafe %s", app.ErrBootstrapUnsupported, label)
		}
	}
	binaryHash, err := hashRegularFile(binaryPath)
	if err != nil {
		return app.BootstrapSpec{}, fmt.Errorf("%w: amcd executable is not a trusted regular file", app.ErrBootstrapUnsupported)
	}
	base := strings.TrimRight(host.LocalAppData, `\/`) + `\AgentMachineControl\bootstrap`
	wrapperPath := base + `\amcd-current-user.ps1`
	metadataPath := base + `\amcd-current-user.json`
	powerShellExecutable := strings.TrimRight(host.SystemRoot, `\/`) + `\System32\WindowsPowerShell\v1.0\powershell.exe`
	if hasPowerShellMetacharacter(host.WSLExecutable) || hasPowerShellMetacharacter(powerShellExecutable) || hasPowerShellMetacharacter(wrapperPath) {
		return app.BootstrapSpec{}, fmt.Errorf("%w: unsafe Windows host path", app.ErrBootstrapUnsupported)
	}
	spec := app.BootstrapSpec{
		TaskPath: taskPath, TaskName: taskName,
		ActionExecutable: powerShellExecutable,
		ActionArguments:  `-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "` + wrapperPath + `"`,
		Account:          identity.Account, UserSID: identity.SID, LogonType: "S4U", RunLevel: "Limited",
		LogonTrigger: true, StartWhenAvailable: true, MultipleInstances: "IgnoreNew",
		RestartCount: 3, RestartInterval: "PT1M", ExecutionTimeLimit: "PT0S",
		AllowStartOnBatteries: true, DontStopOnBatteries: true,
		Distro: distro, LinuxUser: linuxUser, StateDir: stateRoot,
		WrapperPath: wrapperPath, MetadataPath: metadataPath,
		BinaryPath: binaryPath, BinarySHA256: binaryHash,
		WSLExecutable: host.WSLExecutable, ListenAddress: "127.0.0.1:0",
	}
	wrapper, err := wrapperBytes(spec)
	if err != nil {
		return app.BootstrapSpec{}, err
	}
	spec.WrapperSHA256 = hashBytes(wrapper)
	metadata, err := metadataBytes(spec)
	if err != nil {
		return app.BootstrapSpec{}, err
	}
	spec.MetadataSHA256 = hashBytes(metadata)
	if err := spec.Validate(identity); err != nil {
		return app.BootstrapSpec{}, err
	}
	return spec, nil
}

func wrapperBytes(spec app.BootstrapSpec) ([]byte, error) {
	for label, value := range map[string]string{
		"WSL executable": spec.WSLExecutable, "distro": spec.Distro, "Linux user": spec.LinuxUser,
		"binary path": spec.BinaryPath, "state directory": spec.StateDir, "listen address": spec.ListenAddress,
	} {
		if hasPowerShellMetacharacter(value) {
			return nil, fmt.Errorf("%w: unsafe %s", app.ErrBootstrapUnsupported, label)
		}
	}
	launcher := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$arguments = @(
    '-d',
    '%s',
    '--user',
    '%s',
    '--exec',
    '%s',
    'run',
    '--state-dir',
    '%s',
    '--listen',
    '%s',
    '--json'
)
$child = Start-Process -FilePath '%s' -ArgumentList $arguments -NoNewWindow -Wait -PassThru
exit $child.ExitCode
`, quoteWindowsArgument(spec.Distro), quoteWindowsArgument(spec.LinuxUser), quoteWindowsArgument(spec.BinaryPath), quoteWindowsArgument(spec.StateDir), quoteWindowsArgument(spec.ListenAddress), spec.WSLExecutable)
	return []byte(launcher), nil
}

// quoteWindowsArgument preserves one native argv value when Windows PowerShell
// Start-Process joins ArgumentList entries into a single command line.
func quoteWindowsArgument(value string) string {
	if !windowsArgumentNeedsQuoting(value) {
		return value
	}

	var quoted strings.Builder
	quoted.Grow(len(value) + 2)
	quoted.WriteByte('"')

	backslashes := 0
	for _, character := range value {
		if character == '\\' {
			backslashes++
			continue
		}
		if character == '"' {
			quoted.WriteString(strings.Repeat(`\`, backslashes*2+1))
		} else {
			quoted.WriteString(strings.Repeat(`\`, backslashes))
		}
		quoted.WriteRune(character)
		backslashes = 0
	}
	quoted.WriteString(strings.Repeat(`\`, backslashes*2))
	quoted.WriteByte('"')
	return quoted.String()
}

func windowsArgumentNeedsQuoting(value string) bool {
	return value == "" || strings.ContainsAny(value, " \t\"")
}

func metadataBytes(spec app.BootstrapSpec) ([]byte, error) {
	copySpec := spec
	copySpec.MetadataSHA256 = ""
	return json.MarshalIndent(copySpec, "", "  ")
}

func validateSimpleName(label, value string) error {
	if value == "" || len(value) > 128 {
		return fmt.Errorf("%w: invalid %s", app.ErrBootstrapUnsupported, label)
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._-", char) {
			continue
		}
		return fmt.Errorf("%w: invalid %s", app.ErrBootstrapUnsupported, label)
	}
	return nil
}

func hasPowerShellMetacharacter(value string) bool {
	return strings.ContainsAny(value, "\r\n\x00\"'%&|<>^!`$();{}[]")
}

func hashRegularFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

func hashBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}
