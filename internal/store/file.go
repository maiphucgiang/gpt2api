package store

// WriteJSONFile atomically replaces a private JSON resource and syncs its contents.
func WriteJSONFile(path string, value any) error {
	return writeJSON(path, value)
}
