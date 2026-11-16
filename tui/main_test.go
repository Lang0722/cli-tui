package tui

// Rendering under test doesn't depend on the terminal running the tests:
// links show only their label, as in an OSC 8 terminal.
func init() { linkLabelOnly = func() bool { return true } }
