package mountresolver

import (
	"os"
	"path/filepath"
	"strings"
)

// credentialPaths are home-relative places that hold host credentials: SSH
// keys, cloud and cluster logins, registry and package tokens, password
// stores, agent OAuth logins, and container runtimes whose socket is control
// of the host.
var credentialPaths = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".config/gcloud", ".kube", ".docker",
	".netrc", ".git-credentials", ".config/git/credentials", ".npmrc", ".yarnrc.yml",
	".pypirc", ".config/pip", ".cargo/credentials", ".cargo/credentials.toml",
	".gem/credentials", ".m2/settings.xml", ".gradle/gradle.properties",
	".config/gh", ".config/hub", ".config/glab-cli", ".vault-token", ".password-store",
	".terraform.d/credentials.tfrc.json", ".config/op", ".1password",
	".codex/auth.json", ".claude/.credentials.json", ".pi/agent/auth.json",
	".colima", ".lima", ".orbstack", ".rd", "Library/Keychains",
	"Library/Containers/com.docker.docker", "Library/Group Containers",
}

// CredentialPaths returns the credential locations under home plus extra
// (e.g. aisb's own config directory, which holds the CA key and the store).
func CredentialPaths(home string, extra ...string) []string {
	out := make([]string, 0, len(credentialPaths)+len(extra))
	for _, p := range credentialPaths {
		out = append(out, filepath.Join(home, p))
	}
	return append(out, extra...)
}

// exposesCredentials reports whether mounting or copying src would hand a
// sandbox host credentials: src is a socket, or it (followed through
// symlinks) is, lies inside, or contains one of paths.
func exposesCredentials(src string, paths []string) bool {
	if fi, err := os.Stat(src); err == nil && fi.Mode()&os.ModeSocket != 0 {
		return true
	}
	cands := []string{filepath.Clean(src)}
	if real, err := filepath.EvalSymlinks(src); err == nil {
		cands = append(cands, real)
	}
	for _, c := range cands {
		for _, p := range paths {
			for _, q := range resolvedForms(p) {
				if within(c, q) || within(q, c) {
					return true
				}
			}
		}
	}
	return false
}

// resolvedForms is p and, where symlinks lead elsewhere, the resolved path
// (on macOS a temp dir under /var is really under /private/var). A missing
// p is resolved through its parent.
func resolvedForms(p string) []string {
	p = filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(p); err == nil && real != p {
		return []string{p, real}
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil && dir != filepath.Dir(p) {
		return []string{p, filepath.Join(dir, filepath.Base(p))}
	}
	return []string{p}
}

// within reports whether p is dir or below it.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// RefuseCredentials drops each "src:dest" spec whose source exposes
// credentials (see exposesCredentials), warning about each.
func RefuseCredentials(specs, paths []string, log Warner) []string {
	return refuse(specs, func(spec string) string { src, _, _ := strings.Cut(spec, ":"); return src }, paths, "mounting", log)
}

// RefuseCredentialCopies is RefuseCredentials for copies.
func RefuseCredentialCopies(copies []CopySpec, paths []string, log Warner) []CopySpec {
	return refuse(copies, func(cp CopySpec) string { return cp.Source }, paths, "copying", log)
}

func refuse[T any](items []T, source func(T) string, paths []string, verb string, log Warner) []T {
	out := make([]T, 0, len(items))
	for _, it := range items {
		if src := source(it); exposesCredentials(src, paths) {
			if log != nil {
				log.Warn("proxy mode: not " + verb + " " + src + ": it holds or contains host credentials")
			}
			continue
		}
		out = append(out, it)
	}
	return out
}
