package main

import (
	"crypto/sha256"

	"gopkg.in/yaml.v3"
)

// One native decode supplies the policy and the proxy hosts used by entry DNS.
// Individual Go projections remain independent if the combined decode declines.
type decodedSubscriptionPolicy struct {
	policy  sourcePolicy
	servers []string
	dns     map[string]any
}

func decodeSourcePolicy(body []byte, parsed *decodedSubscriptionPolicy) (sourcePolicy, error) {
	if parsed != nil {
		return parsed.policy, nil
	}
	var p sourcePolicy
	err := yaml.Unmarshal(body, &p)
	return p, err
}

type policyPreviewCache struct {
	digest      [sha256.Size]byte
	initialized bool
	native      *decodedSubscriptionPolicy
	fallback    *sourcePolicy
}

// Config import and preview hold app.mu. Compare contents even when a
// refresh replaces a profile at the same path.
func (c *policyPreviewCache) lookup(body []byte) *decodedSubscriptionPolicy {
	digest := sha256.Sum256(body)
	if !c.initialized || digest != c.digest {
		*c = policyPreviewCache{initialized: true, digest: digest, native: nativeSubscriptionPolicy(body)}
	}
	return c.native
}
func (c *policyPreviewCache) read(body []byte) (sourcePolicy, error) {
	if parsed := c.lookup(body); parsed != nil {
		return parsed.policy, nil
	}
	if c.fallback != nil {
		return *c.fallback, nil
	}
	p, err := decodeSourcePolicy(body, nil)
	if err == nil {
		c.fallback = &p
	}
	return p, err
}
