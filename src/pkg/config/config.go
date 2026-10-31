package config

import (
	"os"
	"strconv"
	"strings"
)

// EnvString returns the environment variable value or fallback if empty.
func EnvString(key, fallback string) string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	return val
}

// EnvInt returns the environment variable parsed as an int or fallback.
func EnvInt(key string, fallback int) int {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(val)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
