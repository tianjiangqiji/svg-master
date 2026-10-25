//go:build !windows

package netproxy

func systemSettings() (string, string) { return "", "" }
