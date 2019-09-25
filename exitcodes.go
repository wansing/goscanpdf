package main

type ExitCode byte

// The value is used as number of LED flashes
const (
	ExitSuccess      ExitCode = 0
	ExitSystemError  ExitCode = 1
	ExitNetworkError ExitCode = 2
	ExitNoScanner    ExitCode = 3
	ExitZeroPages    ExitCode = 4
)
