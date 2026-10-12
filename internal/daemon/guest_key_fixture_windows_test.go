//go:build windows

package daemon

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/Horcag/agent-machine-control/internal/statedir"
	"golang.org/x/sys/windows"
)

// Windows material is CurrentUser DPAPI, protected solely for the service SID.
// Never place synthetic plaintext keys on disk or relax the production reader.
func writeGuestFixtureKeyMaterial(t *testing.T, state *statedir.StateDir, material []byte) error {
	t.Helper()
	if len(material) == 0 || len(material) > 16384 {
		return errors.New("synthetic key size invalid")
	}
	descriptor, err := guestFixtureKeyDescriptor(t, state.KeysDir())
	if err != nil {
		return err
	}
	// #nosec G115 -- synthetic material is explicitly bounded to 16 KiB above.
	input := windows.DataBlob{Size: uint32(len(material)), Data: &material[0]}
	var output windows.DataBlob
	if err = windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return err
	}
	if output.Data != nil {
		defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	}
	if output.Data == nil || output.Size == 0 {
		return errors.New("empty synthetic DPAPI material")
	}
	path, err := windows.UTF16PtrFromString(filepath.Join(state.KeysDir(), "default.dpapi"))
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, &attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), filepath.Join(state.KeysDir(), "default.dpapi"))
	defer file.Close()
	protected := unsafe.Slice(output.Data, output.Size)
	count, err := file.Write(protected)
	if err != nil {
		return err
	}
	if count != len(protected) {
		return io.ErrShortWrite
	}
	return file.Sync()
}

func guestFixtureKeyDescriptor(t *testing.T, directory string) (*windows.SECURITY_DESCRIPTOR, error) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	if len(entries) != 0 {
		return nil, errors.New("fixture key directory is not empty")
	}
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid := user.User.Sid
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid.String() + "D:P(A;OICI;FA;;;" + sid.String() + ")")
	if err != nil {
		return nil, err
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return nil, err
	}
	if err = windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, sid, nil, acl, nil); err != nil {
		return nil, err
	}
	return descriptor, nil
}
