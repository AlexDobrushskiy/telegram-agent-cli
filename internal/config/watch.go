package config

import "path/filepath"

// WatchLock serialises watcher check-and-send runs (see Paths.NewWatchLock).
func (p Paths) WatchLock() string { return filepath.Join(p.Home, "watch.lock") }

// WatchersDir holds one directory per watcher (send logs, user PAUSE) inside the data dir.
func (p Paths) WatchersDir() string { return filepath.Join(p.Home, "watchers") }

// WatcherDir is the data-dir directory of one watcher; name must be validated first.
func (p Paths) WatcherDir(name string) string { return filepath.Join(p.WatchersDir(), name) }
