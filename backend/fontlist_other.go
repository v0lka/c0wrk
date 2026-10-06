//go:build !linux

package backend

import "errors"

// errFontListUnavailable is the non-Linux answer from listFontFamilies:
// there is no fontconfig/fc-list probe for the installed-font listing that
// c0wrk implements today (macOS and Windows enumerate fonts through
// platform-native stacks). The RPC surfaces this as Available=false and the
// frontend treats font enumeration as absent on those platforms.
var errFontListUnavailable = errors.New("font family enumeration is only supported on Linux (fontconfig/fc-list)")

// listFontFamilies is the non-Linux stub backing ListFontFamilies; see
// fontlist_linux.go for the real implementation.
func listFontFamilies(monospace bool) ([]string, error) {
	return nil, errFontListUnavailable
}
