//go:build !race

package builder_test

// raceEnabled reports whether the test binary was built with the race
// detector (-race). See race_test.go for details.
const raceEnabled = false
