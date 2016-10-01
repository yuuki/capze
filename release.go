package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/jehiah/go-strftime"
	"github.com/pkg/errors"

	"github.com/yuuki/capdir/osutil"
)

type Release struct {
	Timestamp	string
	DeployDir	string
	ReleasesDir	string
	ReleaseDir	string
	CurrentDir	string
}

func NewRelease(deployDir string) *Release {
	deployDir, _ = filepath.Abs(deployDir)
	currentDir := filepath.Join(deployDir, "current")
	releasesDir := filepath.Join(deployDir, "releases")

	t := time.Now()
	utc, _ := time.LoadLocation("UTC")
	t = t.In(utc)
	timestamp := strftime.Format("%Y%m%d%H%M%S", t)

	releaseDir := filepath.Join(deployDir, "releases", timestamp)

	r := &Release{
		Timestamp: timestamp,
		DeployDir: deployDir,
		ReleasesDir: releasesDir,
		ReleaseDir: releaseDir,
		CurrentDir: currentDir,
	}
	return r
}

// Deploy release
func (r *Release) Deploy(originDir string) error {
	if err := r.Create(originDir); err != nil {
		return errors.Wrap(err, "Failed to create release")
	}
	if err := r.Symlink(); err != nil {
		return errors.Wrap(err, "Failed to symlink release")
	}
	if err := r.Cleanup(); err != nil {
		return errors.Wrap(err, "Failed to cleanup release")
	}
	return nil
}

// Create release directories
func (r *Release) Create(originDir string) error {
	for _, dir := range []string{originDir, r.DeployDir} {
		if !osutil.ExistsDir(dir) {
			return errors.Errorf("No such directory: %s", dir)
		}
	}
	originDir, _ = filepath.Abs(originDir)

	if !osutil.ExistsDir(r.ReleasesDir) {
		if err := os.MkdirAll(r.ReleasesDir, 0755); err != nil {
			return errors.Wrapf(err, "Failed to create releases directory: %s", r.ReleasesDir)
		}
	}
	if osutil.ExistsDir(r.ReleaseDir) {
		return errors.Errorf("%s is already exists", r.ReleaseDir)
	}
	if err := osutil.RunCmd("mv", originDir, r.ReleaseDir); err != nil {
		return errors.Wrapf(err, "Failed to move %s into %s", originDir, r.ReleaseDir)
	}
	return nil
}

func (r *Release) Symlink() error {
	if !osutil.ExistsDir(r.DeployDir) {
		return errors.Errorf("No such directory: %s", r.DeployDir)
	}

	tmpCurrentPath := filepath.Join(r.ReleaseDir, filepath.Base(r.CurrentDir))
	if err := osutil.Symlink(r.ReleaseDir, tmpCurrentPath); err != nil {
		return err
	}
	if err := os.Rename(tmpCurrentPath, r.CurrentDir); err != nil {
		return errors.Wrapf(err, "Failed to switch current: %s => %s", r.ReleaseDir, r.CurrentDir)
	}
	return nil
}

// Clean up old releases
func (r *Release) Cleanup() error {
	return nil
}

