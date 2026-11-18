//go:build race

package stdio

// The race detector slows decoding a huge line about tenfold.
const raceSlowdown = 10
