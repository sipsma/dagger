package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"slices"
	"strings"

	"github.com/dagger/dagger/util/gitutil"
	"github.com/opencontainers/go-digest"
)

const (
	sshAuthSocketDigestVersion         = "ssh-auth-socket-v1"
	sshAuthSocketDigestFallbackVersion = "ssh-auth-socket-fallback-v1"
)

func CanonicalSSHAuthSocketURLs(urls []string) []string {
	seen := map[string]struct{}{}
	canonical := make([]string, 0, len(urls))
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		if parsed, err := gitutil.ParseURL(raw); err == nil {
			user := ""
			if parsed.User != nil {
				user = parsed.User.Username()
			}
			scope := strings.ToLower(parsed.Scheme) + "://" + user + "@" + strings.ToLower(parsed.Host)
			if _, ok := seen[scope]; ok {
				continue
			}
			seen[scope] = struct{}{}
			canonical = append(canonical, scope)
			continue
		}

		scope := strings.ToLower(raw)
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		canonical = append(canonical, scope)
	}
	slices.Sort(canonical)
	return canonical
}

func ScopedSSHAuthSocketDigest(secretSalt []byte, canonicalURLs []string, fingerprints []string) digest.Digest {
	slices.Sort(canonicalURLs)
	slices.Sort(fingerprints)

	mac := hmac.New(sha256.New, secretSalt)
	mac.Write([]byte(sshAuthSocketDigestVersion))
	mac.Write([]byte{0})
	for _, scope := range canonicalURLs {
		mac.Write([]byte(scope))
		mac.Write([]byte{0})
	}
	for _, fingerprint := range fingerprints {
		mac.Write([]byte(fingerprint))
		mac.Write([]byte{0})
	}
	return digest.NewDigestFromBytes(digest.SHA256, mac.Sum(nil))
}

func FallbackScopedSSHAuthSocketDigest(secretSalt []byte, sourceSocketDigest digest.Digest, canonicalURLs []string) digest.Digest {
	slices.Sort(canonicalURLs)

	mac := hmac.New(sha256.New, secretSalt)
	mac.Write([]byte(sshAuthSocketDigestFallbackVersion))
	mac.Write([]byte{0})
	mac.Write([]byte(sourceSocketDigest))
	mac.Write([]byte{0})
	for _, scope := range canonicalURLs {
		mac.Write([]byte(scope))
		mac.Write([]byte{0})
	}
	return digest.NewDigestFromBytes(digest.SHA256, mac.Sum(nil))
}

func ScopedSSHAuthSocketDigestFromStore(
	ctx context.Context,
	query *Query,
	socketStore *SocketStore,
	sourceSocketDigest digest.Digest,
	urls []string,
) (digest.Digest, error) {
	fingerprints, err := socketStore.AgentFingerprints(ctx, sourceSocketDigest)
	if err != nil {
		return "", err
	}
	return ScopedSSHAuthSocketDigest(query.SecretSalt(), CanonicalSSHAuthSocketURLs(urls), fingerprints), nil
}
