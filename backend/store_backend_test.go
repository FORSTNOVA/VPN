package main

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type storeDispatchSpy struct {
	storeBackend
	writes, reads, closes, upserts int
	failure                        error
	timestamp                      int64
	partial                        trafficHistoryReport
}

func (s *storeDispatchSpy) setValue(k, v string) error { s.writes++; return s.failure }
func (s *storeDispatchSpy) getValue(k string) (string, bool, error) {
	s.reads++
	return "", false, s.failure
}
func (s *storeDispatchSpy) Close() error { s.closes++; return s.failure }
func (s *storeDispatchSpy) upsertNodesAt(n []proxyNode, at int64) error {
	s.upserts++
	s.timestamp = at
	return s.failure
}
func (s *storeDispatchSpy) readTrafficHistory(r trafficHistoryReport, k string) (trafficHistoryReport, error) {
	s.reads++
	return s.partial, s.failure
}
func TestStoreFacadeDispatchAndErrors(t *testing.T) {
	failure := errors.New("synthetic outcome uncertain")
	spy := &storeDispatchSpy{failure: failure, partial: trafficHistoryReport{UploadTotal: 42, Rows: []trafficHistoryRow{{ID: "partial"}}}}
	s := &store{backend: spy}
	if err := s.setValue("synthetic", "value"); err != failure || spy.writes != 1 {
		t.Fatal("write retried or error changed")
	}
	before := time.Now().Unix()
	if err := s.upsertNodes(nil); err != failure || spy.upserts != 1 || spy.timestamp < before || spy.timestamp > time.Now().Unix() {
		t.Fatal("timestamp/dispatch")
	}
	got, err := s.readTrafficHistory(trafficHistoryReport{}, "")
	if err != failure || !reflect.DeepEqual(got, spy.partial) || spy.reads != 1 {
		t.Fatal("partial report/error changed")
	}
	params, err := s.loadHealthParams()
	if err != failure || params != defaultHealthParams() || spy.reads != 2 {
		t.Fatal("health defaults/error changed")
	}
	if err = s.Close(); err != failure || spy.closes != 1 || s.backend != spy {
		t.Fatal("close failure must retain owner")
	}
	if err = s.Close(); err != failure || spy.closes != 2 {
		t.Fatal("explicit close retry")
	}
}
func TestStoreFacadeDefaultOwnership(t *testing.T) {
	s := newTestStore(t)
	backend, ok := s.backend.(*goStoreBackend)
	if !ok || backend.db == nil {
		t.Fatal("default is not Go")
	}
	if err := s.setValue("key", "value"); err != nil {
		t.Fatal(err)
	}
	if backend.db.Stats().MaxOpenConnections != 1 {
		t.Fatal("single-connection limit changed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.setValue("key", "again"); err == nil {
		t.Fatal("closed facade silently reopened")
	}
	if s.backend != backend {
		t.Fatal("owner switched")
	}
	var absent *store
	if err := absent.Close(); err != nil {
		t.Fatal(err)
	}
}
