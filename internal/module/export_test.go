package module

// ProcessStarts reports how many module processes this test binary started.
func ProcessStarts() int64 { return processStarts.Load() }
