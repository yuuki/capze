package layout

import (
	"fmt"
	"time"
)

// idLayout is the name of a release: the UTC time of its deployment, to the
// second, as Capistrano names its releases.
const idLayout = "20060102150405"

// validID reports whether name is the name of a release.
func validID(name string) bool {
	if len(name) != len(idLayout) {
		return false
	}
	for _, c := range []byte(name) {
		if c < '0' || c > '9' {
			return false
		}
	}
	_, err := time.Parse(idLayout, name)
	return err == nil
}

// nextID returns the name of a release deployed at now, when newest is the
// name of the newest existing release ("" without one).
//
// The names must sort in the order of the deployments, because that order is
// all capze knows of the history. So when the clock does not give a name
// after newest (two deployments in one second, a clock set back), the name is
// one second after newest instead.
func nextID(now time.Time, newest string) (string, error) {
	id := now.UTC().Format(idLayout)
	if newest != "" && id <= newest {
		t, err := time.Parse(idLayout, newest)
		if err != nil {
			return "", fmt.Errorf("invalid release name %q", newest)
		}
		id = t.Add(time.Second).Format(idLayout)
	}
	if !validID(id) || id <= newest {
		return "", fmt.Errorf("cannot name a release after %q at %s", newest, now.UTC().Format(time.RFC3339))
	}
	return id, nil
}
