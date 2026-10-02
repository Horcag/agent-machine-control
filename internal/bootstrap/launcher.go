package bootstrap

import (
	"debug/pe"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/app"
)

// launcherArtifact pins the consoleless Windows companion shipped beside amcd.
func launcherArtifact(binaryPath, distro string) (string, string, error) {
	path := filepath.Join(filepath.Dir(binaryPath), "amcd-launcher.exe")
	digest, err := hashRegularFile(path)
	if err != nil {
		return "", "", fmt.Errorf("%w: amcd-launcher.exe companion is missing or not a regular file", app.ErrBootstrapUnsupported)
	}
	executable, err := pe.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("%w: launcher is not a Windows GUI executable", app.ErrBootstrapUnsupported)
	}
	defer executable.Close()
	gui := false
	switch header := executable.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		gui = header.Subsystem == pe.IMAGE_SUBSYSTEM_WINDOWS_GUI
	case *pe.OptionalHeader32:
		gui = header.Subsystem == pe.IMAGE_SUBSYSTEM_WINDOWS_GUI
	}
	if !gui {
		return "", "", fmt.Errorf("%w: launcher must use the Windows GUI subsystem", app.ErrBootstrapUnsupported)
	}
	source := `\\wsl.localhost\` + distro + strings.ReplaceAll(path, "/", `\`)
	return source, digest, nil
}
