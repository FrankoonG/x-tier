//go:build !windows

package webbridge

func validPlatformStaticPath(string) bool {
	return true
}
