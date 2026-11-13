package layout

import (
	"testing"
	"time"
)

func TestValidID(t *testing.T) {
	valid := []string{"20261113101500", "20161002074731", "00010101000000", "99991231235959", "20240229000000"}
	for _, name := range valid {
		if !validID(name) {
			t.Errorf("validID(%q) = false", name)
		}
	}
	invalid := []string{
		"", "current", "2026111310150", "202611131015000", "20261113T01500", "20261113101500.1",
		"20261313101500", "20261132101500", "20261113250000", "20261113106000", "20261113101560",
		"20250229000000", "-0261113101500", "2026111310150٠",
	}
	for _, name := range invalid {
		if validID(name) {
			t.Errorf("validID(%q) = true", name)
		}
	}
}

func TestNextID(t *testing.T) {
	now := time.Date(2026, 11, 13, 19, 15, 0, 999, time.FixedZone("JST", 9*60*60))
	tests := []struct {
		name, newest, want string
	}{
		{"first release, in UTC", "", "20261113101500"},
		{"after an older release", "20261113101459", "20261113101500"},
		{"same second", "20261113101500", "20261113101501"},
		{"clock set back", "20270101000000", "20270101000001"},
		{"carry", "20261231235959", "20270101000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextID(now, tt.newest)
			if err != nil || got != tt.want {
				t.Errorf("nextID(%s) = %q, %v, want %q", tt.newest, got, err, tt.want)
			}
		})
	}
	// Past the last name there is none.
	if got, err := nextID(now, "99991231235959"); err == nil {
		t.Errorf("nextID after the last name = %q", got)
	}
	if got, err := nextID(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), ""); err == nil {
		t.Errorf("nextID in the year 10000 = %q", got)
	}
}

// The name of a new release is a release name that sorts after the newest
// one, whatever the clock and the newest one are.
func FuzzNextID(f *testing.F) {
	f.Add(int64(1794564900), "20261113101500")
	f.Add(int64(0), "")
	f.Add(int64(-1), "99991231235959")
	f.Add(int64(253402300800), "00010101000000")
	f.Fuzz(func(t *testing.T, sec int64, newest string) {
		if newest != "" && !validID(newest) {
			t.Skip()
		}
		id, err := nextID(time.Unix(sec, 0), newest)
		if err != nil {
			return
		}
		if !validID(id) || id <= newest {
			t.Errorf("nextID(%d, %q) = %q", sec, newest, id)
		}
	})
}

func TestExpired(t *testing.T) {
	tests := []struct {
		n, current, keep, want int
	}{
		{0, -1, 5, 0},
		{3, 2, 5, 0},
		{5, 4, 5, 0},
		{6, 5, 5, 1},
		{6, 5, 1, 5},
		// After a rollback: counted back from the current release.
		{6, 3, 2, 2},
		{6, 0, 1, 0},
		// Without a current release: the newest ones.
		{6, -1, 2, 4},
	}
	for _, tt := range tests {
		if got := expired(tt.n, tt.current, tt.keep); got != tt.want {
			t.Errorf("expired(%d, %d, %d) = %d, want %d", tt.n, tt.current, tt.keep, got, tt.want)
		}
	}
}

// Pruning never deletes the current release nor a newer one, and keeps as
// many releases as asked when there are that many.
func FuzzExpired(f *testing.F) {
	f.Add(6, 3, 2)
	f.Add(0, -1, 1)
	f.Fuzz(func(t *testing.T, n, current, keep int) {
		if n < 0 || n > 1<<20 || current < -1 || current >= n || keep < 1 {
			t.Skip()
		}
		k := expired(n, current, keep)
		head := n - 1
		if current >= 0 {
			head = current
		}
		switch {
		case k < 0 || k > n:
			t.Errorf("expired(%d, %d, %d) = %d", n, current, keep, k)
		case current >= 0 && k > current:
			t.Errorf("expired(%d, %d, %d) = %d deletes the current release", n, current, keep, k)
		case head+1-k != min(keep, head+1):
			t.Errorf("expired(%d, %d, %d) = %d keeps %d releases", n, current, keep, k, head+1-k)
		}
	})
}
