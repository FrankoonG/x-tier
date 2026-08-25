//go:build !linux

package configstore

func pinConfigPath(path string) (string, func() error, error) {
	return path, func() error { return nil }, nil
}
