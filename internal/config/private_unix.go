//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// checkPrivateDir refuses a config folder that another user could write to
// or that is a symlink: whoever controls the folder can swap config.json.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("dossier de config %s : lien symbolique refusé", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("dossier de config %s : pas un dossier", dir)
	}
	if err := ownedByMe(dir, fi); err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("dossier de config %s : droits %o trop larges (attendu 700 : chmod 700 %q)", dir, fi.Mode().Perm(), dir)
	}
	return nil
}

// checkPrivateFile refuses a config file that is a symlink, not a regular
// file, owned by someone else, or readable/writable by group or others.
func checkPrivateFile(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("config %s : pas un fichier ordinaire (lien symbolique ?) — refusé", path)
	}
	if err := ownedByMe(path, fi); err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("config %s : droits %o trop larges (attendu 600 : chmod 600 %q)", path, fi.Mode().Perm(), path)
	}
	return nil
}

func ownedByMe(path string, fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := os.Getuid(); int(st.Uid) != uid {
		return fmt.Errorf("%s appartient à l’uid %d, pas à vous (uid %d) — refusé", path, st.Uid, uid)
	}
	return nil
}
