//go:build db2cli

package db2util

import _ "github.com/ibmdb/go_ibm_db"

func driverAvailable() bool { return true }
