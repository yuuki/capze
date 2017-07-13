package release

import (
	"os"
	"regexp"
	"testing"
)

func TestNewRelease(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	cases := []struct {
		inDeployPath    string
		outDeployPath   string
		outReleasesPath string
		outCurrentPath  string
		desc            string
	}{
		{"/var/www/app", "/var/www/app", "/var/www/app/releases", "/var/www/app/current", "absolute path"},
		{"www/app", cwd + "/www/app", cwd + "/www/app/releases", cwd + "/www/app/current", "relative path"},
	}
	for _, tc := range cases {
		r := NewRelease(tc.inDeployPath)

		if len(r.Timestamp) != 14 {
			t.Errorf("timestamp length got %v, want %v", len(r.Timestamp), 14)
		}
		matched, err := regexp.MatchString(`^[0-9]{14}$`, r.Timestamp)
		if err != nil {
			panic(err)
		}
		if !matched {
			t.Errorf("timestamp should be numeric string. got %s", r.Timestamp)
		}
		if r.DeployPath != tc.outDeployPath {
			t.Errorf("deploy path got %v, want %v", r.DeployPath, tc.outDeployPath)
		}
		if r.ReleasesPath != tc.outReleasesPath {
			t.Errorf("deploy path got %v, want %v", r.ReleasesPath, tc.outReleasesPath)
		}
		if r.ReleasePath != tc.outReleasesPath+"/"+r.Timestamp {
			t.Errorf("deploy path got %v, want %v", r.ReleasePath, tc.outReleasesPath+"/"+r.Timestamp)
		}
		if r.CurrentPath != tc.outCurrentPath {
			t.Errorf("deploy path got %v, want %v", r.CurrentPath, tc.outCurrentPath)
		}
	}

}
