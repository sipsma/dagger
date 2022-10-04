package version

import (
	"errors"
	"runtime/debug"
)

var ErrNoBuildInfo = errors.New("no build info available")

// Revision returns the VCS revision being used to build or empty string
func Revision() (string, error) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ErrNoBuildInfo
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			return s.Value[:9], nil
		}
	}
	return "", nil
}
