package piruntime

import "strings"

// installerEnvironment is only for the pinned dependency build, never for pi
// or an explicit wrapper. Public npm downloads need OS paths and the user's
// registry/proxy/CA configuration, not model, GitHub or local agent credentials.
// No App vault material is added to the parent's environment in the first place.
func installerEnvironment(parent []string) []string {
	allowed := map[string]bool{}
	for _, key := range []string{
		"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
		"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "TMP", "TEMP", "TMPDIR", "LANG", "LC_ALL",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
		"NPM_CONFIG_REGISTRY", "NPM_CONFIG_USERCONFIG", "NPM_CONFIG_GLOBALCONFIG", "NPM_CONFIG_PROXY", "NPM_CONFIG_HTTPS_PROXY",
		"NPM_CONFIG_NOPROXY", "NPM_CONFIG_CAFILE", "NPM_CONFIG_CA", "NPM_CONFIG_STRICT_SSL",
	} {
		allowed[key] = true
	}
	var env []string
	for _, entry := range parent {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[strings.ToUpper(key)] {
			env = append(env, entry)
		}
	}
	return env
}
