package release

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jehiah/go-strftime"
	"github.com/pkg/errors"

	"github.com/yuuki/capze/osutil"
)

type Release struct {
	Timestamp    string
	DeployPath   string
	ReleasesPath string
	ReleasePath  string
	CurrentPath  string
}

func NewRelease(deployPath string) *Release {
	deployPath, _ = filepath.Abs(deployPath)
	currentPath := filepath.Join(deployPath, "current")
	releasesPath := filepath.Join(deployPath, "releases")

	t := time.Now()
	utc, _ := time.LoadLocation("UTC")
	t = t.In(utc)
	timestamp := strftime.Format("%Y%m%d%H%M%S", t)

	releasePath := filepath.Join(deployPath, "releases", timestamp)

	r := &Release{
		Timestamp:    timestamp,
		DeployPath:   deployPath,
		ReleasesPath: releasesPath,
		ReleasePath:  releasePath,
		CurrentPath:  currentPath,
	}
	return r
}

func (r *Release) SetReleasePath(timestamp string) {
	r.ReleasePath = filepath.Join(r.DeployPath, "releases", timestamp)
}

// Deploy release
func (r *Release) Deploy(originPath string, keep int, prune bool) error {
	if err := r.Create(originPath); err != nil {
		return errors.Wrap(err, "Failed to create release")
	}
	if err := r.Symlink(); err != nil {
		return errors.Wrap(err, "Failed to symlink release")
	}
	if prune {
		if err := r.Cleanup(keep); err != nil {
			return errors.Wrap(err, "Failed to cleanup release")
		}
	}
	return nil
}

// Create release directories
func (r *Release) Create(originPath string) error {
	for _, dir := range []string{originPath, r.DeployPath} {
		if !osutil.ExistsDir(dir) {
			return errors.Errorf("No such directory: %s", dir)
		}
	}
	originPath, _ = filepath.Abs(originPath)

	if !osutil.ExistsDir(r.ReleasesPath) {
		if err := os.MkdirAll(r.ReleasesPath, 0755); err != nil {
			return errors.Wrapf(err, "Failed to create releases directory: %s", r.ReleasesPath)
		}
	}
	if osutil.ExistsDir(r.ReleasePath) {
		return errors.Errorf("%s is already exists", r.ReleasePath)
	}
	if err := osutil.RunCmd("mv", originPath, r.ReleasePath); err != nil {
		return errors.Wrapf(err, "Failed to move %s into %s", originPath, r.ReleasePath)
	}
	return nil
}

func (r *Release) Symlink() error {
	if !osutil.ExistsDir(r.DeployPath) {
		return errors.Errorf("No such directory: %s", r.DeployPath)
	}

	tmpCurrentPath := filepath.Join(r.ReleasePath, filepath.Base(r.CurrentPath))
	if err := osutil.Symlink(r.ReleasePath, tmpCurrentPath); err != nil {
		return err
	}
	if err := os.Rename(tmpCurrentPath, r.CurrentPath); err != nil {
		return errors.Wrapf(err, "Failed to switch current: %s => %s", r.ReleasePath, r.CurrentPath)
	}
	return nil
}

// Clean up old releases
func (r *Release) Cleanup(keep int) error {
	if !osutil.ExistsDir(r.DeployPath) {
		return errors.Errorf("No such directory: %s", r.DeployPath)
	}

	dirs, err := r.findPrunedDirs(keep)
	if err != nil {
		return err
	}

	if len(dirs) > 0 {
		var dirsStr string
		for _, dir := range dirs {
			dirsStr = strings.Join([]string{dirsStr, dir}, " ")
		}
		rmCmd := fmt.Sprintf("rm -fr %s", dirsStr)
		if err := osutil.RunCmd("/bin/bash", "-c", rmCmd); err != nil {
			return errors.Wrapf(err, "Failed to remove %s", dirsStr)
		}
		fmt.Printf("deleted: %s\n", dirsStr)
	}

	return nil
}

// Rollback to old release
func (r *Release) Rollback() error {
	if !osutil.ExistsDir(r.DeployPath) {
		return errors.Errorf("No such directory: %s", r.DeployPath)
	}

	out, err := exec.Command("ls", "-1t", r.ReleasesPath).Output()
	if err != nil {
		return errors.Wrapf(err, "Failed to list releases %s", r.ReleasesPath)
	}
	timestamps := strings.Split(string(out), "\n")
	if len(timestamps) < 2 {
		return errors.Errorf("There are no older releases to rollback to %s", r.ReleasesPath)
	}

	index := -1
	if v := os.Getenv("ROLLBACK_RELEASE"); v == "" {
		index = 1
	} else {
		for i, t := range timestamps {
			if v == t {
				index = i
				break
			}
		}
		if index == -1 {
			return errors.Errorf("Cannot rollback because release %s does not exist", v)
		}
	}

	last := timestamps[index]

	r.SetReleasePath(last)
	if err := r.Symlink(); err != nil {
		return errors.Errorf("Failed to switch symlink for rollback to %s", r.ReleasePath)
	}

	return nil
}

// PrunedDirs returns the string slice of directories that it will be pruned.
func (r *Release) PrunedDirs(keep int) ([]string, error) {
	if !osutil.ExistsDir(r.DeployPath) {
		return []string{}, errors.Errorf("No such directory: %s", r.DeployPath)
	}
	return r.findPrunedDirs(keep)
}

func (r *Release) findPrunedDirs(keep int) ([]string, error) {
	out, err := exec.Command("ls", "-1tr", r.ReleasesPath).Output()
	if err != nil {
		return []string{}, errors.Wrapf(err, "Failed to list releases %s", r.ReleasesPath)
	}
	timestamps := strings.Split(string(out), "\n")
	if len(timestamps) > keep {
		n := len(timestamps) - 1 - keep
		dirs := make([]string, 0, n)
		for _, ts := range timestamps[0:n] {
			dirs = append(dirs, filepath.Join(r.ReleasesPath, ts))
		}
		return dirs, nil
	}

	return []string{}, nil
}
