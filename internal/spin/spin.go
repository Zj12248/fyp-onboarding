package spin

import "time"

// SpinFor busy-loops until the duration elapses (CPU-bound).
func SpinFor(d time.Duration) {
	deadline := time.Now().Add(d)
	x := 0.0
	for time.Now().Before(deadline) {
		// Do some floating point ops to keep CPU hot.
		x = x*1.0000001 + 3.14159
		if x > 1e9 {
			x = 0
		}
	}
}
