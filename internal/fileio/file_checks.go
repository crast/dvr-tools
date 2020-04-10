package fileio

import "os"

func IsFile(f string) bool {
	fi, err := os.Stat(f)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		panic(err)
	}
	return !fi.IsDir()
}
