package main

import (
	"encoding/json"
	"os"
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
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := serviceRecordPath(home)
	temp := path + ".tmp"
	if err := os.WriteFile(temp, body, 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}


func removeServiceRecord(home string, pid int) {
	body, err := os.ReadFile(serviceRecordPath(home))
	if err != nil {
		return
	}
	var record serviceRecord
	if json.Unmarshal(body, &record) == nil && record.PID != pid {
		return
	}
	_ = os.Remove(serviceRecordPath(home))
}
