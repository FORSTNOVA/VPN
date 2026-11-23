package main

import (
	"fmt"
	"time"
)

// A store owns exactly one backend for its entire lifetime. Calls never retry
// on another backend after an error: a write may already have committed.
type storeBackend interface {
	Close() error
	upsertNodesAt([]proxyNode, int64) error
	saveRegion(regionRecord) error
	loadRegions() (map[string]regionRecord, error)
	clearRegions() error
	saveHealth(nodeHealth) error
	loadHealth() (map[string]nodeHealth, error)
	clearHealth() error
	appendSwitchEvent(switchEvent) error
	recentSwitchEvents(int) ([]switchEvent, error)
	setValue(string, string) error
	getValue(string) (string, bool, error)
	saveProbeRecord(probeRecord) error
	loadProbeRecords() ([]probeRecord, error)
	recordTraffic(string, kernelTraffic, time.Time) error
	readTrafficHistory(trafficHistoryReport, string) (trafficHistoryReport, error)
}

var _ storeBackend = (*goStoreBackend)(nil)

type store struct{ backend storeBackend }

// Backend selection happens before opening and stays fixed for this store instance.
func openStore(path string) (*store, error) {
	backend, err := selectStoreBackend(path)
	if err != nil {
		return nil, err
	}
	return &store{backend: backend}, nil
}
func (s *store) Close() error {
	if s == nil || s.backend == nil {
		return nil
	}
	return s.backend.Close()
}
func (s *store) upsertNodes(nodes []proxyNode) error {
	return s.backend.upsertNodesAt(nodes, unixSeconds(time.Now()))
}
func (s *store) saveRegion(r regionRecord) error               { return s.backend.saveRegion(r) }
func (s *store) loadRegions() (map[string]regionRecord, error) { return s.backend.loadRegions() }
func (s *store) clearRegions() error                           { return s.backend.clearRegions() }
func (s *store) saveHealth(r nodeHealth) error                 { return s.backend.saveHealth(r) }
func (s *store) loadHealth() (map[string]nodeHealth, error)    { return s.backend.loadHealth() }
func (s *store) clearHealth() error                            { return s.backend.clearHealth() }
func (s *store) appendSwitchEvent(e switchEvent) error         { return s.backend.appendSwitchEvent(e) }
func (s *store) recentSwitchEvents(n int) ([]switchEvent, error) {
	return s.backend.recentSwitchEvents(n)
}
func (s *store) setValue(k, v string) error               { return s.backend.setValue(k, v) }
func (s *store) getValue(k string) (string, bool, error)  { return s.backend.getValue(k) }
func (s *store) saveProbeRecord(r probeRecord) error      { return s.backend.saveProbeRecord(r) }
func (s *store) loadProbeRecords() ([]probeRecord, error) { return s.backend.loadProbeRecords() }
func (s *store) recordTraffic(session string, k kernelTraffic, now time.Time) error {
	return s.backend.recordTraffic(session, k, now)
}
func (s *store) readTrafficHistory(r trafficHistoryReport, key string) (trafficHistoryReport, error) {
	return s.backend.readTrafficHistory(r, key)
}

func (s *store) saveHealthParams(params healthParams) error {
	body, err := params.encode()
	if err != nil {
		return err
	}
	return s.setValue("health_params", body)
}

func (s *store) loadHealthParams() (healthParams, error) {
	params := defaultHealthParams()
	body, found, err := s.getValue("health_params")
	if err != nil || !found {
		return params, err
	}
	if err := params.decode(body); err != nil {
		return defaultHealthParams(), fmt.Errorf("stored health parameters were unreadable: %w", err)
	}
	return params.normalized(), nil
}
