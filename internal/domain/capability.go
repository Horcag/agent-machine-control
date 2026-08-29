package domain

import (
	"sort"
)

// Capability is a strongly typed, validated capability identifier.
type Capability string

// String returns the string representation of the Capability.
func (c Capability) String() string {
	return string(c)
}

// Validate checks that the Capability is non-empty, bounded, and contains only valid characters.
func (c Capability) Validate() error {
	return ValidateCapability(string(c))
}

// CapabilitySet represents a set of advertised or available backend capabilities.
type CapabilitySet map[string]struct{}

// NewCapabilitySet creates a CapabilitySet from a list of capability strings.
func NewCapabilitySet(caps ...string) CapabilitySet {
	set := make(CapabilitySet, len(caps))
	for _, c := range caps {
		if c != "" {
			set[c] = struct{}{}
		}
	}
	return set
}

// Validate checks that every capability in the set is a valid canonical identifier.
func (cs CapabilitySet) Validate() error {
	for c := range cs {
		if err := ValidateCapability(c); err != nil {
			return err
		}
	}
	return nil
}

// Has returns true if the capability is present in the set.
func (cs CapabilitySet) Has(capability string) bool {
	if cs == nil {
		return false
	}
	_, ok := cs[capability]
	return ok
}

// IsSubsetOf returns true if every capability in cs is present in other.
func (cs CapabilitySet) IsSubsetOf(other CapabilitySet) bool {
	if len(cs) == 0 {
		return true
	}
	if len(other) == 0 {
		return false
	}
	for k := range cs {
		if !other.Has(k) {
			return false
		}
	}
	return true
}

// Clone returns a deep copy of the CapabilitySet.
func (cs CapabilitySet) Clone() CapabilitySet {
	if cs == nil {
		return make(CapabilitySet)
	}
	clone := make(CapabilitySet, len(cs))
	for k := range cs {
		clone[k] = struct{}{}
	}
	return clone
}

// Slice returns a sorted slice of capabilities in the set.
func (cs CapabilitySet) Slice() []string {
	if len(cs) == 0 {
		return nil
	}
	res := make([]string, 0, len(cs))
	for k := range cs {
		res = append(res, k)
	}
	sort.Strings(res)
	return res
}
