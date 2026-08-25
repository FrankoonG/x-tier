//go:build !linux

package daemon

func pinInstanceConfigPath(path string) (string, func() error, error) {
	return path, func() error { return nil }, nil
}
