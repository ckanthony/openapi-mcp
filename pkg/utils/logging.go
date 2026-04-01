package utils

import (
	"log"
	"strings"
)

// SanitizeLogInput sanitizes user input for logging to prevent log forging
func SanitizeLogInput(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", "\\n"), "\r", "\\r")
}

// SafeLogPrintf logs with sanitized string arguments to prevent log forging
func SafeLogPrintf(format string, args ...interface{}) {
	for i, arg := range args {
		if s, ok := arg.(string); ok {
			args[i] = SanitizeLogInput(s)
		}
	}
	log.Printf(format, args...)
}

// SafeLogPrintln logs with sanitized string arguments to prevent log forging, like log.Println
func SafeLogPrintln(args ...interface{}) {
	for i, arg := range args {
		if s, ok := arg.(string); ok {
			args[i] = SanitizeLogInput(s)
		}
	}
	log.Println(args...)
}

// SafeLogFatalf logs with sanitized string arguments to prevent log forging
func SafeLogFatalf(format string, args ...interface{}) {
	for i, arg := range args {
		if s, ok := arg.(string); ok {
			args[i] = SanitizeLogInput(s)
		}
	}
	log.Fatalf(format, args...)
}
