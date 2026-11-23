package main

// probeRecord is the persisted representation, not an HTTP response. JSON is
// deliberately opaque here: legacy malformed rows keep their existing handling
// in the application layer, rather than being silently dropped by storage.
type probeRecord struct {
	Path        string
	Fingerprint string
	Delay       int64
	Error       string
	Measured    int64
	Details     string
}

func (s *goStoreBackend) saveProbeRecord(record probeRecord) error {
	_, err := s.db.Exec("INSERT INTO probe_results(path,fingerprint,delay,error,measured,details) VALUES(?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET fingerprint=excluded.fingerprint,delay=excluded.delay,error=excluded.error,measured=excluded.measured,details=excluded.details", record.Path, record.Fingerprint, record.Delay, record.Error, record.Measured, record.Details)
	return err
}

func (s *goStoreBackend) loadProbeRecords() ([]probeRecord, error) {
	rows, err := s.db.Query("SELECT path,fingerprint,delay,error,measured,details FROM probe_results ORDER BY measured DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []probeRecord{}
	for rows.Next() {
		var record probeRecord
		if err := rows.Scan(&record.Path, &record.Fingerprint, &record.Delay, &record.Error, &record.Measured, &record.Details); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
