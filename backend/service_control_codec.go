package main

import "errors"

// The typed wire uses raw byte strings and preserves nil versus empty lists.
const serviceControlMaxBytes = 8 << 20

func writeServiceControl(w *servicePathWriter, v serviceControlState) {
	w.flag(v.ImportPolicy)
	w.strings(v.GameBypass)
	w.flag(v.DomesticWeb)
	w.flag(v.WebRTCProtection)
	w.flag(v.DNSProtection)
	w.str(v.SubscriptionURL)
	w.str(v.MihomoPath)
	w.str(v.SelectedNode)
	w.str(v.SelectionMode)
	w.str(v.LockedRegion)
	w.str(v.LockedNode)
	w.str(v.ConnectionMode)
	w.flag(v.StrictPrivacy)
	w.str(v.WintunPath)
	w.n(uint64(v.AutoMergeHours))
	w.n(uint64(v.SocksPort))
	w.str(v.RuntimeLockedRegion)
	w.str(v.RuntimeLockedNode)
}
func readServiceControl(r *servicePathReader) serviceControlState {
	return serviceControlState{
		ImportPolicy:        r.flag(),
		GameBypass:          r.strings(),
		DomesticWeb:         r.flag(),
		WebRTCProtection:    r.flag(),
		DNSProtection:       r.flag(),
		SubscriptionURL:     r.str(),
		MihomoPath:          r.str(),
		SelectedNode:        r.str(),
		SelectionMode:       r.str(),
		LockedRegion:        r.str(),
		LockedNode:          r.str(),
		ConnectionMode:      r.str(),
		StrictPrivacy:       r.flag(),
		WintunPath:          r.str(),
		AutoMergeHours:      r.integerPlatform(),
		SocksPort:           r.integerPlatform(),
		RuntimeLockedRegion: r.str(),
		RuntimeLockedNode:   r.str(),
	}
}
func encodeServiceControlState(v serviceControlState) (string, error) {
	w := servicePathWriter{}
	writeServiceControl(&w, v)
	return w.finish()
}
func decodeServiceControlState(b string) (serviceControlState, error) {
	if b == "" {
		return serviceControlState{}, nil
	}
	if len(b) > serviceControlMaxBytes {
		return serviceControlState{}, errors.New("control size limit")
	}
	r := servicePathReader{quotaReader{b: []byte(b)}}
	v := readServiceControl(&r)
	return v, serviceWireDone(&r.quotaReader)
}
func validControlCommand(c serviceControlCommand) bool {
	return (c.Action == 1 || c.Action == 2) && c.Mask&^uint64(controlAll) == 0 && (c.Action != 1 || c.Mask == controlAll)
}
func encodeServiceControlCommand(c serviceControlCommand) (string, error) {
	if !validControlCommand(c) {
		return "", errors.New("invalid control command")
	}
	w := servicePathWriter{b: []byte{c.Action}}
	w.n(c.Mask)
	writeServiceControl(&w, c.Value)
	return w.finish()
}
func decodeServiceControlCommand(b string) (serviceControlCommand, error) {
	if len(b) > serviceControlMaxBytes {
		return serviceControlCommand{}, errors.New("control size limit")
	}
	r := servicePathReader{quotaReader{b: []byte(b)}}
	c := serviceControlCommand{Action: r.byte(), Mask: r.n()}
	c.Value = readServiceControl(&r)
	if e := serviceWireDone(&r.quotaReader); e != nil {
		return c, e
	}
	if !validControlCommand(c) {
		return c, errors.New("invalid control command")
	}
	return c, nil
}
