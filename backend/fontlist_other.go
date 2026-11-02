//go:build !linux

package backend

import "errors"

// errFontListUnavailable is the non-Linux answer from listFontFamilies:
// installed-font enumeration is Linux/fontconfig-only in c0wrk; the RPC
// surfaces Available=false and the frontend treats enumeration as absent.
var errFontListUnavailable = errors.New("font family enumeration is only supported on Linux (fontconfig/fc-list)")

// listFontFamilies is the non-Linux stub backing ListFontFamilies; see
// fontlist_linux.go for the real implementation.
func listFontFamilies(monospace bool) ([]string, error) {
	return nil, errFontListUnavailable
}
