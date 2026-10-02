package statedir

// EnsurePrivateDirectory applies the platform privacy and symlink checks with private file inheritance.
func EnsurePrivateDirectory(path string) error {
	if handled, err := ensurePlatformStateDirectories([]string{path}, path); err != nil {
		return err
	} else if handled {
		return nil
	}
	return ensureSingleDir(path, true)
}
