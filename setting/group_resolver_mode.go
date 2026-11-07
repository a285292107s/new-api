package setting

import (
	"os"
	"strings"
)

// Group registry feature gates. Every migrated read path keeps its legacy
// implementation behind one of these switches so an operator can fall back
// without redeploying: legacy (default off) -> shadow -> registry.

// groupResolverMode returns the configured mode for one subsystem.
//
// Accepted values: "" / "legacy" (use the old implementation), "shadow" (run
// both and compare), "registry" (use the new one). The per-subsystem env var
// wins over the global GROUP_RESOLVER_MODE.
func groupResolverMode(subsystem string) string {
	if value := strings.TrimSpace(os.Getenv("GROUP_RESOLVER_" + strings.ToUpper(subsystem))); value != "" {
		return strings.ToLower(value)
	}
	if value := strings.TrimSpace(os.Getenv("GROUP_RESOLVER_MODE")); value != "" {
		return strings.ToLower(value)
	}
	// Default to legacy. Turning the registry on is an explicit rollout step
	// (per subsystem or globally), so a deploy can never silently change auth or
	// billing semantics with no shadow observation period.
	return "legacy"
}

// GroupResolverUsesRegistry reports whether a subsystem should read the new
// registry. It is intentionally cheap (env lookup only) so request paths can
// call it freely.
func GroupResolverUsesRegistry(subsystem string) bool {
	return groupResolverMode(subsystem) == "registry"
}

// GroupResolverInShadow reports whether a subsystem should compare both
// implementations. Shadow mode never changes the response.
func GroupResolverInShadow(subsystem string) bool {
	return groupResolverMode(subsystem) == "shadow"
}
