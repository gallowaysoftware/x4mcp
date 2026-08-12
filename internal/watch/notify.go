package watch

import (
	"context"
	"slices"

	"github.com/fsnotify/fsnotify"
)

// fsWatcher is the filesystem-notification seam: enough of fsnotify.Watcher to
// drive the accelerator, and small enough that a test can be one.
//
// Directories are watched, never files. X4 does not modify a save in place and
// leave it there — it writes, renames, rotates — so a watch on a path is a
// watch on a file that will shortly not be the file anymore. A directory watch
// survives all of that, and survives the directory itself being removed and
// recreated because the watch list is re-synced on every check.
type fsWatcher interface {
	Add(dir string) error
	Remove(dir string) error
	WatchList() []string
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Close() error
}

// fsnotifyWatcher adapts *fsnotify.Watcher, whose Events/Errors are fields.
type fsnotifyWatcher struct{ *fsnotify.Watcher }

func (f fsnotifyWatcher) Events() <-chan fsnotify.Event { return f.Watcher.Events }
func (f fsnotifyWatcher) Errors() <-chan error          { return f.Watcher.Errors }

func newFSNotifyWatcher() (fsWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return fsnotifyWatcher{w}, nil
}

// startNotify brings the accelerator up, or records why it could not and
// carries on.
//
// Failure here is NOT a startup failure. inotify watch limits, an unsupported
// filesystem, a container with the syscall blocked — every one of those is a
// machine where the poll alone is still correct, just slower. The one thing
// that must never happen is refusing to watch saves because the optimisation
// was unavailable, so this logs once, puts the reason in the health payload,
// and returns.
func (w *Watcher) startNotify(ctx context.Context) {
	fs, err := w.opts.newFS()
	if err != nil {
		w.opts.Logf("WARN: filesystem watcher unavailable (%v); polling every %s instead — detection stays correct, just slower", err, w.opts.Poll)
		w.bump(func(st *state) { st.notifyErr = err.Error(); st.notifyActive = false })
		return
	}
	w.mu.Lock()
	w.fs = fs
	w.st.notifyActive = true
	w.st.notifyErr = ""
	w.mu.Unlock()

	go w.notifyLoop(ctx, fs)
}

// notifyLoop turns every event into a poke and nothing else.
//
// The event KIND is deliberately not inspected. A save arrives as some
// unknowable sequence of create/write/rename/chmod — under Proton it is Wine
// translating Windows file ops, and the pattern is not knowable in advance or
// stable across game versions — so reading meaning into it would be inventing
// a contract the source never offered. "Something happened in a save dir" is
// the entire signal; the settle gate decides what it means.
func (w *Watcher) notifyLoop(ctx context.Context, fs fsWatcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-fs.Events():
			if !ok {
				w.bump(func(st *state) { st.notifyActive = false })
				return
			}
			w.bump(func(st *state) {
				st.events++
				st.lastEventAt = w.clock.Now()
			})
			_ = ev // the path is not needed: the checker re-stats everything
			select {
			case w.pokes <- sourceNotify:
			default: // a check is already pending; that is what a poke asks for
			}
		case err, ok := <-fs.Errors():
			if !ok {
				w.bump(func(st *state) { st.notifyActive = false })
				return
			}
			// Overflow and watch-limit errors are exactly the case the poll
			// exists for: record it, keep the watcher, keep polling.
			w.opts.Logf("WARN: filesystem watcher error: %v (the poll still covers this)", err)
			w.bump(func(st *state) { st.notifyErr = err.Error() })
		}
	}
}

// syncWatches makes the watched set equal dirs. Called from every check, which
// is what recovers from a save directory being deleted and recreated (a
// reinstall, a Proton prefix rebuild): fsnotify drops the watch on removal and
// nothing would ever re-add it otherwise.
func (w *Watcher) syncWatches(dirs []string) {
	w.mu.Lock()
	fs := w.fs
	w.mu.Unlock()
	if fs == nil {
		return
	}
	have := fs.WatchList()
	for _, d := range dirs {
		if !slices.Contains(have, d) {
			if err := fs.Add(d); err != nil {
				w.opts.Logf("WARN: cannot watch %s (%v); polling covers it", d, err)
				w.bump(func(st *state) { st.notifyErr = err.Error() })
			}
		}
	}
	for _, d := range have {
		if !slices.Contains(dirs, d) {
			_ = fs.Remove(d)
		}
	}
	n := len(fs.WatchList())
	w.bump(func(st *state) { st.notifyDirs = n })
}

func (w *Watcher) closeFS() {
	w.mu.Lock()
	fs := w.fs
	w.fs = nil
	w.st.notifyActive = false
	w.mu.Unlock()
	if fs != nil {
		_ = fs.Close()
	}
}
