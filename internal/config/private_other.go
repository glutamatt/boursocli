//go:build !unix

package config

import "os"

// On Windows the user profile folder ACL already keeps the config private;
// POSIX modes and uids do not apply.
func checkPrivateDir(dir string) error {
	_, err := os.Stat(dir)
	return err
}

func checkPrivateFile(path string) error {
	_, err := os.Stat(path)
	return err
}
