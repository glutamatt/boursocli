//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// checkPrivateDir refuses a config folder in which another user could
// replace config.json: one that belongs to another user (root excepted), or
// that group/others may write to without the sticky bit. Read bits do not
// matter — the file itself is 0600 — so a 0755 project folder, a 0750
// home or /tmp (1777) are fine. Symlinks to the folder are followed: what
// counts is the real folder.
func checkPrivateDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("dossier de config %s : pas un dossier", dir)
	}
	mode := fi.Mode()
	if mode&os.ModeSticky != 0 {
		return nil // /tmp-like: others cannot rename or remove our file
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() && st.Uid != 0 {
		return fmt.Errorf("dossier de config %s : appartient à l’uid %d, pas à vous (uid %d) — refusé", dir, st.Uid, os.Getuid())
	}
	if mode.Perm()&0o022 != 0 {
		return fmt.Errorf("dossier de config %s : droits %o, d’autres peuvent y écrire (chmod go-w %q)", dir, mode.Perm(), dir)
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
