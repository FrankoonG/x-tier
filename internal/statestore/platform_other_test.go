//go:build !windows

package statestore

import (
	"os"
)

func renameOpenDirectoryForTest(source, target string) error {
	return os.Rename(source, target)
}

func openAncestorRenameBlockedForTest(error) bool {
	return false
}
