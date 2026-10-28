package config

import "path/filepath"

// WatchLock serialises watcher check-and-send runs (see Paths.NewWatchLock).
func (p Paths) WatchLock() string { return filepath.Join(p.Home, "watch.lock") }
