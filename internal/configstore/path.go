package configstore

// CanonicalPath returns a platform-canonical absolute path. An existing target
// must pass the same security checks as Load; a missing target is allowed.
func CanonicalPath(path string) (string, error) {
	if path == "" {
		return "", configErrorf("config.path_required")
	}
	return canonicalPath(path)
}
