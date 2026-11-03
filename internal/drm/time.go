package drm

import "time"

// nowUnix returns the current Unix timestamp as a float64.
func nowUnix() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
