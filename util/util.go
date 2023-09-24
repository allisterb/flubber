package util

import (
	"os"
	"path/filepath"
)

var AppData = filepath.Join(GetUserHomeDir(), ".flubber")

var ServerConfigFile = filepath.Join(AppData, "node.json")

var DbDir = filepath.Join(AppData, "db")

var ClientConfigFile = filepath.Join(AppData, "client.json")

var Shutdown = false

func GetUserHomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return h
}

func Contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func PathExists(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	} else {
		return true
	}
}
