package funkutil

// LibBackupPath returns the per-binary library-backup sidecar path
// (<safePath>.libbackup.json), mapping each library this install merged
// debug info into (original absolute path) to where its pre-merge original
// was backed up, so uninstall can restore it.
func LibBackupPath(safePath string) string { return safePath + ".libbackup.json" }

// WriteLibBackups writes the backup map as JSON to LibBackupPath(safePath).
// A nil/empty map deletes any existing sidecar.
func WriteLibBackups(safePath string, backups map[string]string) error {
	return writeJSON(LibBackupPath(safePath), backups, func(v map[string]string) bool { return len(v) == 0 })
}

// ReadLibBackups reads LibBackupPath(safePath) and returns the backup map.
// Missing or malformed sidecars yield nil (no error).
func ReadLibBackups(safePath string) map[string]string {
	return readJSON[map[string]string](LibBackupPath(safePath))
}
