//go:build race

package builder_test

// raceDetectorEnabled reports whether the test binary was built with the race
// detector (-race). It is used to skip tests whose peak memory is too large for
// the race detector's shadow-memory overhead to fit on CI runners.
const raceDetectorEnabled = true
