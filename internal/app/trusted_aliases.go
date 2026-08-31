package app

import (
	"fmt"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// SetMachineAliases assigns exact server-owned aliases to a canonical machine.
func (i *TrustedInventory) SetMachineAliases(locator domain.MachineLocator, aliases []string) error {
	normalized, err := normalizeMachineAliases(locator, aliases)
	if err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	entry, err := i.validateMachineAliasesLocked(locator, normalized)
	if err != nil {
		return err
	}
	entry.Aliases = normalized
	i.machines[locator.String()] = entry
	return nil
}

// ValidateMachineAliases checks exact alias ownership without mutating the inventory.
func (i *TrustedInventory) ValidateMachineAliases(locator domain.MachineLocator, aliases []string) error {
	normalized, err := normalizeMachineAliases(locator, aliases)
	if err != nil {
		return err
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	_, err = i.validateMachineAliasesLocked(locator, normalized)
	return err
}

func normalizeMachineAliases(locator domain.MachineLocator, aliases []string) ([]string, error) {
	if err := locator.Validate(); err != nil {
		return nil, err
	}
	normalized := make([]string, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		value, err := domain.NormalizeExactAlias(alias)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("%w: duplicate machine alias %q", domain.ErrInvalidAlias, value)
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func (i *TrustedInventory) validateMachineAliasesLocked(locator domain.MachineLocator, normalized []string) (MachineIndexEntry, error) {
	entry, exists := i.machines[locator.String()]
	if !exists {
		return MachineIndexEntry{}, domain.ErrMachineReferenceMiss
	}
	seen := make(map[string]struct{}, len(normalized))
	for _, alias := range normalized {
		seen[alias] = struct{}{}
	}
	if err := rejectCanonicalAliasCollisions(i.machines, seen); err != nil {
		return MachineIndexEntry{}, err
	}
	for key, candidate := range i.machines {
		if key == locator.String() {
			continue
		}
		if _, collision := seen[candidate.DisplayName]; collision {
			return MachineIndexEntry{}, fmt.Errorf("%w: machine alias %q collides", domain.ErrMachineReferenceAmbig, candidate.DisplayName)
		}
		for _, alias := range candidate.Aliases {
			if _, collision := seen[alias]; collision {
				return MachineIndexEntry{}, fmt.Errorf("%w: machine alias %q collides", domain.ErrMachineReferenceAmbig, alias)
			}
		}
	}
	return entry, nil
}

func rejectCanonicalAliasCollisions(entries map[string]MachineIndexEntry, aliases map[string]struct{}) error {
	for key, entry := range entries {
		if _, collision := aliases[key]; collision {
			return fmt.Errorf("%w: alias %q collides with canonical locator", domain.ErrMachineReferenceAmbig, key)
		}
		if _, collision := aliases[entry.Locator.VMID]; collision {
			return fmt.Errorf("%w: alias %q collides with canonical VM GUID", domain.ErrMachineReferenceAmbig, entry.Locator.VMID)
		}
	}
	return nil
}
