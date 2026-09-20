//go:build race

package builder_test

// raceEnabled reports whether the test binary was built with the race
// detector (-race). It is used to skip tests whose memory footprint, once
// multiplied by the race detector's shadow memory, exceeds the memory
// available on CI runners.
const raceEnabled = true
