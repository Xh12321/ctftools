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
