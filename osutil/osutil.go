package osutil

import (
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"github.com/pkg/errors"

	"github.com/yuuki/capdir/log"
)

func ExistsFile(file string) bool {
	f, err := os.Stat(file)
	return err == nil && !f.IsDir()
}

func IsSymlink(file string) bool {
	f, err := os.Lstat(file)
	return err == nil && f.Mode()&os.ModeSymlink == os.ModeSymlink
}

func ExistsDir(dir string) bool {
	if f, err := os.Stat(dir); os.IsNotExist(err) || !f.IsDir() {
		return false
	}
	return true
}

func IsDirEmpty(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		log.Debugf("Failed to open %s: %s\n", dir, err)
		return false
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true
	}
	return false
}

func RunCmd(name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return errors.Wrapf(err, "Failed to get stderr pipe %s %s", name, arg)
	}
	if err := cmd.Start(); err != nil {
		return errors.Wrapf(err, "Failed to exec %s %s", name, arg)
	}

	b, err := ioutil.ReadAll(stderr)
	if err != nil {
		return errors.New("Failed to read stderr")
	}
	errmsg := strings.TrimRight(string(b), "\n")

	if err := cmd.Wait(); err != nil {
		return errors.New(errmsg)
	}
	return nil
}

func Cp(from, to string) error {
	if err := RunCmd("cp", "-p", from, to); err != nil {
		return err
	}
	return nil
}

// Symlink, but ignore already exists file.
func Symlink(oldname, newname string) error {
	log.Debug("symlink", oldname, newname)
	if err := os.Symlink(oldname, newname); err != nil {
		// Ignore already created symlink
		if _, ok := err.(*os.LinkError); !ok {
			return errors.Wrapf(err, "Failed to symlink %s %s", oldname, newname)
		}
	}
	return nil
}

