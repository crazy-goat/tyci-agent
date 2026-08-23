package agent

// GetSidebarVisible returns the persisted sidebar visibility from
// ~/.tyci/config.json (sidebar_visible). Missing config or key means false:
// the sidebar stays closed unless the user has explicitly opened (and thus
// saved) it at least once.
func GetSidebarVisible() bool {
	return LoadTyciConfig().SidebarVisible
}

// SetSidebarVisible persists the sidebar visibility to ~/.tyci/config.json,
// reloading first so a concurrent tyci session's other fields aren't
// clobbered — same reload-then-save posture as SetFavoriteModels.
func SetSidebarVisible(visible bool) error {
	cfg := LoadTyciConfig()
	cfg.SidebarVisible = visible
	return SaveTyciConfig(cfg)
}
