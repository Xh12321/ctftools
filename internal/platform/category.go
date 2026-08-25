package platform

import (
	"fmt"
	"strings"
)

// Category is one of the six supported CTF challenge directions.
type Category string

const (
	CategoryWeb       Category = "web"
	CategoryCrypto    Category = "crypto"
	CategoryPwn       Category = "pwn"
	CategoryReverse   Category = "reverse"
	CategoryForensics Category = "forensics"
	CategoryMisc      Category = "misc"
)

var validCategories = map[Category]struct{}{
	CategoryWeb:       {},
	CategoryCrypto:    {},
	CategoryPwn:       {},
	CategoryReverse:   {},
	CategoryForensics: {},
	CategoryMisc:      {},
}

// AllCategories returns the canonical ordered list of challenge categories.
func AllCategories() []Category {
	return []Category{
		CategoryWeb,
		CategoryCrypto,
		CategoryPwn,
		CategoryReverse,
		CategoryForensics,
		CategoryMisc,
	}
}

// ParseCategory normalizes and validates a category string.
func ParseCategory(raw string) (Category, error) {
	c := Category(strings.ToLower(strings.TrimSpace(raw)))
	if _, ok := validCategories[c]; !ok {
		return "", fmt.Errorf("invalid category %q", raw)
	}
	return c, nil
}

// DefaultImage returns the default sandbox image tag for a category.
func (c Category) DefaultImage(version string) string {
	if version == "" {
		version = "0.1.0"
	}
	return fmt.Sprintf("ctf-agent-pi-%s:%s", string(c), version)
}

// String implements fmt.Stringer.
func (c Category) String() string { return string(c) }

// DefaultSandboxPolicy returns the standard strict sandbox security policy for category.
func DefaultSandboxPolicy(cat Category, imageOverride string) SandboxPolicy {
	img := imageOverride
	if strings.TrimSpace(img) == "" {
		img = cat.DefaultImage("0.1.0")
	}

	base := SandboxPolicy{
		Category:         cat,
		Image:            img,
		CPUQuotaCores:    2.0,
		MemoryLimitMB:    1024,
		PidsLimit:        256,
		Capabilities:     []string{},
		DropCaps:         []string{"ALL"},
		AllowPtrace:      false,
		AllowNetwork:     false,
		DefaultUser:      "ctf",
		MountWorkspace:   "/workspace",
		MountSkills:      "/opt/cpi/ctf-skills",
		ReadonlySkills:   true,
		ForbidDockerSock: true,
	}

	switch cat {
	case CategoryWeb:
		base.AllowNetwork = true
		base.Capabilities = []string{"NET_BIND_SERVICE"}
	case CategoryCrypto:
		base.MemoryLimitMB = 2048
		base.PidsLimit = 128
		base.AllowNetwork = false
	case CategoryPwn:
		base.AllowNetwork = true
		base.AllowPtrace = true
		base.Capabilities = []string{"SYS_PTRACE"}
	case CategoryReverse:
		base.MemoryLimitMB = 2048
		base.AllowNetwork = false
		base.AllowPtrace = true
		base.Capabilities = []string{"SYS_PTRACE"}
	case CategoryForensics:
		base.MemoryLimitMB = 2048
		base.AllowNetwork = false
	case CategoryMisc:
		base.MemoryLimitMB = 1024
		base.PidsLimit = 128
		base.AllowNetwork = false
	}

	return base
}
