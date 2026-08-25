package configstore

import (
	"errors"
	"fmt"
	"io"
	"os"
)

var ErrInsecureFile = errors.New("config: insecure file")

const maxConfigFileBytes = 16 << 20

func readSecureFile(path string) ([]byte, error) {
	return ReadProtectedFile(path, maxConfigFileBytes)
}

// ReadProtectedFile reads a regular, single-link file owned by the current
// identity whose permissions are restricted to the platform's trusted local
// principals. It opens without following reparse points or symbolic links.
func ReadProtectedFile(path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 1 {
		return nil, &os.PathError{Op: "read", Path: path, Err: configErrorf("config.file_limit_invalid")}
	}
	file, err := openSecureConfigFile(path)
	if err != nil {
		return nil, err
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(contents)) > maxBytes {
		return nil, &os.PathError{
			Op:   "read",
			Path: path,
			Err:  configErrorf("config.file_too_large: limit=%d", maxBytes),
		}
	}
	return contents, nil
}

func insecureFileError(path, reason string) error {
	return &os.PathError{
		Op:   "validate",
		Path: path,
		Err:  fmt.Errorf("%w: %s", ErrInsecureFile, reason),
	}
}
