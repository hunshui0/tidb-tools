//go:build !db2cli

package db2util

func driverAvailable() bool { return false }
