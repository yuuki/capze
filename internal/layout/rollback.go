package layout

import (
	"context"
	"fmt"
)

// Rollback points current back to the release before the current one, or to
// the release named to if named is true, and returns that release.
//
// Rollback deletes nothing. The releases newer than the new current one stay
// until the next deployment discards them: until then, Rollback can return to
// one of them by its name.
func (l *Layout) Rollback(ctx context.Context, to string, named bool) (Release, error) {
	h, err := l.open()
	if err != nil {
		return Release{}, err
	}
	defer h.close()
	// Not a deploy directory, or not yet: nothing is created in it.
	if h.releases == nil {
		return Release{}, fmt.Errorf("no release in %s", l.releasesPath())
	}
	if err := h.ensureState(); err != nil {
		return Release{}, err
	}
	unlock, err := h.state.Lock(ctx, l.Waiting)
	if err != nil {
		return Release{}, err
	}
	defer unlock()

	hist, err := l.scan(h)
	if err != nil {
		return Release{}, err
	}
	if err := l.checkCurrentLink(hist); err != nil {
		return Release{}, err
	}
	target, err := l.rollbackTarget(hist, to, named)
	if err != nil {
		return Release{}, err
	}
	if target != hist.current {
		if err := h.prepareSwitch(hist.releases[target].ID); err != nil {
			return Release{}, err
		}
		if err := ctx.Err(); err != nil {
			h.cancelSwitch()
			return Release{}, err
		}
		if err := l.commitSwitch(h); err != nil {
			return Release{}, err
		}
	}
	release := hist.releases[target]
	release.State = Current
	return release, nil
}

// rollbackTarget returns the index of the release that a rollback makes
// current.
func (l *Layout) rollbackTarget(hist *history, to string, named bool) (int, error) {
	if named {
		// An empty name is not "the release before": it is what an unset
		// variable of a script gives.
		if !validID(to) {
			return 0, fmt.Errorf("%q is not the name of a release", to)
		}
		target := hist.find(to)
		if target < 0 {
			return 0, fmt.Errorf("no release %s in %s", to, l.releasesPath())
		}
		return target, nil
	}
	switch {
	case len(hist.releases) == 0:
		return 0, fmt.Errorf("no release in %s", l.releasesPath())
	case hist.current < 0:
		return 0, fmt.Errorf("%s does not point to a release of %s: name the release with --to", l.currentPath(), l.releasesPath())
	case hist.current == 0:
		return 0, fmt.Errorf("no release older than the current one, %s", hist.releases[0].ID)
	}
	return hist.current - 1, nil
}
