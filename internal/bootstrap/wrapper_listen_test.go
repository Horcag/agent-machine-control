package bootstrap

import (
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func TestWrapperAutomaticListenerOmitsAddressArguments(t *testing.T) {
	spec := app.BootstrapSpec{WSLExecutable: `C:\Windows\System32\wsl.exe`, Distro: "Synthetic-WSL", LinuxUser: "operator", BinaryPath: "/bin/amcd", StateDir: "/state"}
	for _, address := range []string{"", "127.0.0.1:0"} {
		spec.ListenAddress = address
		body, err := wrapperBytes(spec)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "'--listen'") != (address != "") {
			t.Fatalf("wrong automatic/explicit listener arguments for %q", address)
		}
		if !strings.Contains(string(body), "'--state-dir',\n    '/state',") || !strings.Contains(string(body), "'--json'") {
			t.Fatal("automatic listener lost adjacent native arguments")
		}
	}
}
