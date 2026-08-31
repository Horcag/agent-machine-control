package app

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const defaultHostQueryTimeout = 15 * time.Second

const localHostAddress = "local"

// HostHealth describes the current read-only availability of a trusted host.
type HostHealth string

const (
	HostHealthObserved     HostHealth = "observed"
	HostHealthStale        HostHealth = "stale"
	HostHealthUnavailable  HostHealth = "host_unavailable"
	HostHealthAccessDenied HostHealth = "access_denied"
)

// HostEntry is an operator-approved Hyper-V host route.
type HostEntry struct {
	ID           domain.HostID
	Address      string
	Enabled      bool
	Aliases      []string
	QueryTimeout time.Duration
}

// Validate checks host registry invariants without probing the network.
func (h HostEntry) Validate() error {
	if err := h.ID.Validate(); err != nil {
		return err
	}
	if h.ID == domain.LocalHostID && h.Address != localHostAddress {
		return fmt.Errorf("%w: local host entry address must be %q", domain.ErrInvalidHostAddress, localHostAddress)
	}
	if err := domain.ValidateHostAddress(h.Address); err != nil {
		return err
	}
	if h.QueryTimeout < 0 {
		return fmt.Errorf("app: query timeout cannot be negative")
	}
	seen := make(map[string]struct{}, len(h.Aliases))
	for _, alias := range h.Aliases {
		normalized, err := domain.NormalizeExactAlias(alias)
		if err != nil {
			return err
		}
		if _, exists := seen[normalized]; exists {
			return fmt.Errorf("%w: duplicate host alias %q", domain.ErrInvalidAlias, normalized)
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

func (h HostEntry) effectiveQueryTimeout() time.Duration {
	if h.QueryTimeout > 0 {
		return h.QueryTimeout
	}
	return defaultHostQueryTimeout
}

// MachineIndexStatus records whether a machine identity is current and routeable.
type MachineIndexStatus string

const (
	MachineIndexObserved        MachineIndexStatus = "observed"
	MachineIndexStale           MachineIndexStatus = "stale"
	MachineIndexHostUnavailable MachineIndexStatus = "host_unavailable"
	MachineIndexAccessDenied    MachineIndexStatus = "access_denied"
)

// MachineIndexEntry stores last-known identity and current route status.
type MachineIndexEntry struct {
	Locator        domain.MachineLocator
	DisplayName    string
	Aliases        []string
	LastObservedAt time.Time
	LastStatus     MachineIndexStatus
	Observation    domain.MachineObservation
}

// Validate checks machine index invariants.
func (m MachineIndexEntry) Validate() error {
	if err := m.Locator.Validate(); err != nil {
		return err
	}
	if m.DisplayName != "" {
		if err := domain.ValidateBoundedString(m.DisplayName, 1, 256, domain.ErrInvalidMachineName); err != nil {
			return err
		}
	}
	for _, alias := range m.Aliases {
		if _, err := domain.NormalizeExactAlias(alias); err != nil {
			return err
		}
	}
	if m.Observation.ID != "" {
		if err := m.Observation.Validate(); err != nil {
			return err
		}
		if m.Observation.HostID != m.Locator.HostID || m.Observation.Locator != m.Locator {
			return fmt.Errorf("%w: nested observation locator does not match index entry", domain.ErrInvalidMachineLocator)
		}
	}
	switch m.LastStatus {
	case MachineIndexObserved:
		if m.LastObservedAt.IsZero() {
			return fmt.Errorf("%w: observed machine must have observation timestamp", domain.ErrInvalidObservationTimestamp)
		}
		if m.Observation.ID == "" {
			return fmt.Errorf("%w: observed machine must carry nested observation", domain.ErrInvalidMachineLocator)
		}
	case MachineIndexStale, MachineIndexHostUnavailable, MachineIndexAccessDenied:
	default:
		return fmt.Errorf("app: invalid machine index status %q", m.LastStatus)
	}
	return nil
}

// HostSnapshot is the per-host result of a read-only refresh.
type HostSnapshot struct {
	HostID   domain.HostID
	Health   HostHealth
	Machines []domain.MachineObservation
	Err      error
}

// TrustedInventory is an operator-owned in-memory registry and machine index.
type TrustedInventory struct {
	mu       sync.RWMutex
	hosts    map[domain.HostID]HostEntry
	machines map[string]MachineIndexEntry
}

// NewTrustedInventory validates host entries and creates an empty machine index.
func NewTrustedInventory(hosts []HostEntry) (*TrustedInventory, error) {
	inv := &TrustedInventory{
		hosts:    make(map[domain.HostID]HostEntry, len(hosts)),
		machines: make(map[string]MachineIndexEntry),
	}
	if err := inv.ReplaceHosts(hosts); err != nil {
		return nil, err
	}
	return inv, nil
}

// ReplaceHosts atomically replaces the operator-owned host registry.
func (i *TrustedInventory) ReplaceHosts(hosts []HostEntry) error {
	hosts = withAutomaticLocalHost(hosts)
	next := make(map[domain.HostID]HostEntry, len(hosts))
	hostAliases := make(map[string]domain.HostID)
	for _, host := range hosts {
		if err := host.Validate(); err != nil {
			return err
		}
		if _, exists := next[host.ID]; exists {
			return fmt.Errorf("%w: duplicate host ID %s", domain.ErrInvalidHostID, host.ID)
		}
		for _, alias := range host.Aliases {
			normalized, _ := domain.NormalizeExactAlias(alias)
			if owner, exists := hostAliases[normalized]; exists && owner != host.ID {
				return fmt.Errorf("%w: host alias %q collides", domain.ErrInvalidAlias, normalized)
			}
			hostAliases[normalized] = host.ID
		}
		host.Aliases = cloneStrings(host.Aliases)
		next[host.ID] = host
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.hosts = next
	return nil
}

func withAutomaticLocalHost(hosts []HostEntry) []HostEntry {
	for _, host := range hosts {
		if host.ID == domain.LocalHostID {
			return hosts
		}
	}
	next := make([]HostEntry, 0, len(hosts)+1)
	next = append(next, HostEntry{ID: domain.LocalHostID, Address: localHostAddress, Enabled: true})
	next = append(next, hosts...)
	return next
}

// Hosts returns enabled and disabled trusted hosts in deterministic order.
func (i *TrustedInventory) Hosts() []HostEntry {
	i.mu.RLock()
	defer i.mu.RUnlock()
	hosts := make([]HostEntry, 0, len(i.hosts))
	for _, host := range i.hosts {
		host.Aliases = cloneStrings(host.Aliases)
		hosts = append(hosts, host)
	}
	sort.Slice(hosts, func(a, b int) bool {
		if hosts[a].ID == domain.LocalHostID || hosts[b].ID == domain.LocalHostID {
			return hosts[a].ID == domain.LocalHostID
		}
		return hosts[a].ID < hosts[b].ID
	})
	return hosts
}

// ApplySnapshot merges a per-host refresh, preserving last-known identities on failure.
func (i *TrustedInventory) ApplySnapshot(snapshot HostSnapshot) error {
	if err := snapshot.HostID.Validate(); err != nil {
		return err
	}
	status := statusFromHealth(snapshot.Health)
	if status == "" {
		return fmt.Errorf("app: invalid host health %q", snapshot.Health)
	}
	if status == MachineIndexObserved && snapshot.Err != nil {
		return fmt.Errorf("app: observed host snapshot cannot carry error: %w", snapshot.Err)
	}
	if status == MachineIndexObserved {
		nextEntries, err := buildObservedEntries(snapshot.HostID, snapshot.Machines)
		if err != nil {
			return err
		}
		return i.applyObservedSnapshot(snapshot.HostID, nextEntries)
	}
	return i.applyHostStatus(snapshot.HostID, status)
}

func buildObservedEntries(hostID domain.HostID, machines []domain.MachineObservation) (map[string]MachineIndexEntry, error) {
	nextEntries := make(map[string]MachineIndexEntry, len(machines))
	for _, obs := range machines {
		entry, err := observedEntry(hostID, obs)
		if err != nil {
			return nil, err
		}
		key := entry.Locator.String()
		if _, exists := nextEntries[key]; exists {
			return nil, fmt.Errorf("%w: duplicate VM ID %s on host %s", domain.ErrMachineReferenceAmbig, entry.Locator.VMID, hostID)
		}
		nextEntries[key] = entry
	}
	return nextEntries, nil
}

func observedEntry(hostID domain.HostID, obs domain.MachineObservation) (MachineIndexEntry, error) {
	normalizedVMID, err := domain.NormalizeMachineGUID(obs.ID)
	if err != nil {
		return MachineIndexEntry{}, err
	}
	locator, err := domain.NewMachineLocator(hostID, normalizedVMID)
	if err != nil {
		return MachineIndexEntry{}, err
	}
	cloned := obs.Clone()
	cloned.ID = normalizedVMID
	cloned.HostID = hostID
	cloned.Locator = locator
	if err := cloned.Validate(); err != nil {
		return MachineIndexEntry{}, err
	}
	entry := MachineIndexEntry{
		Locator:        locator,
		DisplayName:    cloned.Name,
		LastObservedAt: cloned.ObservedAt,
		LastStatus:     MachineIndexObserved,
		Observation:    cloned,
	}
	if err := entry.Validate(); err != nil {
		return MachineIndexEntry{}, err
	}
	return entry, nil
}

func (i *TrustedInventory) applyObservedSnapshot(hostID domain.HostID, nextEntries map[string]MachineIndexEntry) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, exists := i.hosts[hostID]; !exists {
		return fmt.Errorf("%w: snapshot for unregistered host %s", domain.ErrInvalidHostID, hostID)
	}
	for key, entry := range i.machines {
		if entry.Locator.HostID != hostID {
			continue
		}
		if _, stillPresent := nextEntries[key]; !stillPresent {
			entry.LastStatus = MachineIndexStale
			i.machines[key] = entry
		}
	}
	for key, entry := range nextEntries {
		if previous, exists := i.machines[key]; exists {
			entry.Aliases = cloneStrings(previous.Aliases)
		}
		i.machines[key] = entry
	}
	return nil
}

func (i *TrustedInventory) applyHostStatus(hostID domain.HostID, status MachineIndexStatus) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, exists := i.hosts[hostID]; !exists {
		return fmt.Errorf("%w: snapshot for unregistered host %s", domain.ErrInvalidHostID, hostID)
	}
	for key, entry := range i.machines {
		if entry.Locator.HostID == hostID {
			entry.LastStatus = status
			i.machines[key] = entry
		}
	}
	return nil
}

// ResolveMachine resolves a canonical locator, GUID, exact display name, or exact alias.
func (i *TrustedInventory) ResolveMachine(reference string) (MachineIndexEntry, error) {
	ref := strings.TrimSpace(reference)
	if ref == "" {
		return MachineIndexEntry{}, domain.ErrMachineReferenceEmpty
	}
	if ref != reference {
		return MachineIndexEntry{}, domain.ErrMachineReferenceMiss
	}
	if locator, err := domain.ParseMachineLocator(ref); err == nil {
		return i.resolveCanonical(locator)
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	var matches []MachineIndexEntry
	if guid, err := domain.NormalizeMachineGUID(ref); err == nil {
		for _, entry := range i.machines {
			if entry.Locator.VMID == guid {
				matches = append(matches, entry)
			}
		}
	} else {
		for _, entry := range i.machines {
			if entry.DisplayName == ref || containsExact(entry.Aliases, ref) {
				matches = append(matches, entry)
			}
		}
	}
	return i.selectOne(matches)
}

// ResolveSingleLocal returns the only current enabled local machine.
func (i *TrustedInventory) ResolveSingleLocal() (MachineIndexEntry, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	matches := make([]MachineIndexEntry, 0, 1)
	for _, entry := range i.machines {
		if entry.Locator.HostID == domain.LocalHostID {
			matches = append(matches, entry)
		}
	}
	return i.selectOne(matches)
}

// CurrentLocalMachines returns every current enabled local machine in stable canonical order.
func (i *TrustedInventory) CurrentLocalMachines() ([]MachineIndexEntry, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	matches := make([]MachineIndexEntry, 0)
	for _, entry := range i.machines {
		if entry.Locator.HostID != domain.LocalHostID || entry.LastStatus != MachineIndexObserved {
			continue
		}
		host, ok := i.hosts[entry.Locator.HostID]
		if !ok || !host.Enabled {
			continue
		}
		entry.Aliases = cloneStrings(entry.Aliases)
		entry.Observation = entry.Observation.Clone()
		matches = append(matches, entry)
	}
	sort.Slice(matches, func(a, b int) bool { return matches[a].Locator.String() < matches[b].Locator.String() })
	return matches, nil
}

func (i *TrustedInventory) resolveCanonical(locator domain.MachineLocator) (MachineIndexEntry, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	entry, exists := i.machines[locator.String()]
	if !exists {
		return MachineIndexEntry{}, domain.ErrMachineReferenceMiss
	}
	return i.selectOne([]MachineIndexEntry{entry})
}

func (i *TrustedInventory) selectOne(matches []MachineIndexEntry) (MachineIndexEntry, error) {
	if len(matches) > 1 {
		return MachineIndexEntry{}, domain.ErrMachineReferenceAmbig
	}
	current := make([]MachineIndexEntry, 0, len(matches))
	var disabled, stale, unavailable, denied bool
	for _, entry := range matches {
		host, exists := i.hosts[entry.Locator.HostID]
		if !exists || !host.Enabled {
			disabled = true
			continue
		}
		switch entry.LastStatus {
		case MachineIndexObserved:
			current = append(current, entry)
		case MachineIndexStale:
			stale = true
		case MachineIndexHostUnavailable:
			unavailable = true
		case MachineIndexAccessDenied:
			denied = true
		}
	}
	if len(current) == 1 {
		entry := current[0]
		entry.Aliases = cloneStrings(entry.Aliases)
		entry.Observation = entry.Observation.Clone()
		return entry, nil
	}
	switch {
	case disabled:
		return MachineIndexEntry{}, domain.ErrMachineHostDisabled
	case denied:
		return MachineIndexEntry{}, domain.ErrMachineAccessDenied
	case unavailable:
		return MachineIndexEntry{}, domain.ErrMachineHostUnavailable
	case stale:
		return MachineIndexEntry{}, domain.ErrMachineReferenceStale
	default:
		return MachineIndexEntry{}, domain.ErrMachineReferenceMiss
	}
}

func statusFromHealth(health HostHealth) MachineIndexStatus {
	switch health {
	case HostHealthObserved:
		return MachineIndexObserved
	case HostHealthStale:
		return MachineIndexStale
	case HostHealthUnavailable:
		return MachineIndexHostUnavailable
	case HostHealthAccessDenied:
		return MachineIndexAccessDenied
	default:
		return ""
	}
}

func containsExact(values []string, needle string) bool {
	return slices.Contains(values, needle)
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}
