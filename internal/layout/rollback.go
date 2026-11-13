package layout

import (
	"context"
	"fmt"
)

// Rollback points current back to the release before the current one, or to
// the release named to if to is not empty, and returns that release.
//
// Rollback deletes nothing. The releases newer than the new current one stay
// until the next deployment discards them: until then, Rollback can return to
// one of them by its name.
func (l *Layout) Rollback(ctx context.Context, to string) (Release, error) {
	unlock, err := l.lock(ctx)
	if err != nil {
		return Release{}, err
	}
	defer unlock()

	h, err := l.scan()
	if err != nil {
		return Release{}, err
	}
	if err := l.checkCurrentLink(); err != nil {
		return Release{}, err
	}
	target, err := l.rollbackTarget(h, to)
	if err != nil {
		return Release{}, err
	}
	if target != h.current {
		if err := l.prepareSwitch(h.releases[target].ID); err != nil {
			return Release{}, err
		}
		if err := ctx.Err(); err != nil {
			l.cancelSwitch()
			return Release{}, err
		}
		if err := l.commitSwitch(); err != nil {
			return Release{}, err
		}
	}
	release := h.releases[target]
	release.State = Current
	return release, nil
}

// rollbackTarget returns the index of the release that a rollback makes
// current.
func (l *Layout) rollbackTarget(h *history, to string) (int, error) {
	if to != "" {
		if !validID(to) {
			return 0, fmt.Errorf("%q is not the name of a release", to)
		}
		target := h.find(to)
		if target < 0 {
			return 0, fmt.Errorf("no release %s in %s", to, l.releasesPath())
		}
		return target, nil
	}
	switch {
	case len(h.releases) == 0:
		return 0, fmt.Errorf("no release in %s", l.releasesPath())
	case h.current < 0:
		return 0, fmt.Errorf("%s does not point to a release of %s: name the release with --to", l.currentPath(), l.releasesPath())
	case h.current == 0:
		return 0, fmt.Errorf("no release older than the current one, %s", h.releases[0].ID)
	}
	return h.current - 1, nil
}
