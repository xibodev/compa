package launcherconfig

import "sync"

// fileMu serializes reads and load-modify-save cycles of launcher-config.json
// in this process: the settings API and the password store both write it, and
// Save rewrites the file in place.
var fileMu sync.Mutex

// Read loads the launcher settings like Load, but never while Update is
// rewriting the file.
func Read(path string, fallback Config) (Config, error) {
	fileMu.Lock()
	defer fileMu.Unlock()
	return Load(path, fallback)
}

// Update loads the launcher settings at path (fallback when the file is
// missing), lets change modify them and saves them, all under one lock, so
// concurrent writers cannot drop each other's changes. A change that returns
// an error saves nothing.
func Update(path string, fallback Config, change func(*Config) error) (Config, error) {
	fileMu.Lock()
	defer fileMu.Unlock()
	cfg, err := Load(path, fallback)
	if err != nil {
		return Config{}, err
	}
	if err := change(&cfg); err != nil {
		return Config{}, err
	}
	if err := Save(path, cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
