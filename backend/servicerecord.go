package main

import (
	"path/filepath"
)

// serviceRecord is the address a running service publishes so that a later
// launch can reuse it instead of starting a second kernel and proxy stack.
// Leaving the old instance in charge would be worse than untidy: it can still
// hold its own Mihomo and TUN, and no UI would be able to reach it any more.
type serviceRecord struct {
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token"`
	// The service binary's identity when this instance started, so a relaunch
	// after a rebuild can tell that the running one is out of date.
	ExeSize  int64 `json:"exeSize,omitempty"`
	ExeMtime int64 `json:"exeMtime,omitempty"`
}

func serviceRecordPath(home string) string {
	return filepath.Join(home, "service.json")
}

func writeServiceRecord(home string, record serviceRecord) error {
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "write-record"})
	e.home = home
	e.record = record
	return runBootstrapWithEffects(e.q, e).Err
}

func removeServiceRecord(home string, pid int) {
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "remove-record"})
	e.home = home
	e.hooks = map[string]func(bootstrapServiceEffect) bootstrapServiceReply{"record-own": func(bootstrapServiceEffect) bootstrapServiceReply {
		return bootstrapServiceReply{Flag: e.record.PID == pid}
	}}
	if r := runBootstrapWithEffects(e.q, e); r.Err != nil {
		panic(r.Err)
	}
}
