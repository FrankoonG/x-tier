//go:build windows

package webbridge

import "strings"

func validPlatformStaticPath(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") ||
			strings.ContainsAny(segment, `<>:"\|?*`) {
			return false
		}
		for _, character := range segment {
			if character < 0x20 {
				return false
			}
		}
		base := segment
		if dot := strings.IndexByte(base, '.'); dot >= 0 {
			base = base[:dot]
		}
		base = strings.ToUpper(base)
		switch base {
		case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
			return false
		}
		if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
			base[3] >= '1' && base[3] <= '9' {
			return false
		}
	}
	return true
}
