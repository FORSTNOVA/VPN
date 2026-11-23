package main

import "errors"

const serviceChinaMaxBytes = 1 << 20

func chinaWireStatus(w *servicePathWriter, s chinaIPStatus) {
	if s.Refreshing || s.Active {
		w.bad = true
	}
	w.flag(s.Available)
	w.n(uint64(s.Entries))
	w.str(s.UpdatedAt)
	w.str(s.Source)
	w.str(s.Error)
}
func chinaReadStatus(r *servicePathReader) chinaIPStatus {
	return chinaIPStatus{Available: r.flag(), Entries: r.integerPlatform(), UpdatedAt: r.str(), Source: r.str(), Error: r.str()}
}
func chinaWireFinish(w *servicePathWriter) (string, error) {
	b, e := w.finish()
	if len(b) > serviceChinaMaxBytes {
		return "", errors.New("china size limit")
	}
	return b, e
}
func encodeServiceChinaState(v serviceChinaState) (string, error) {
	w := servicePathWriter{}
	chinaWireStatus(&w, v.Status)
	w.flag(v.Fetching)
	w.flag(v.Probed)
	w.flag(v.RuleOK)
	w.flag(v.Active)
	return chinaWireFinish(&w)
}
func decodeServiceChinaState(b string) (serviceChinaState, error) {
	if b == "" {
		return serviceChinaState{}, nil
	}
	if len(b) > serviceChinaMaxBytes {
		return serviceChinaState{}, errors.New("china size limit")
	}
	r := servicePathReader{quotaReader{b: []byte(b)}}
	v := serviceChinaState{Status: chinaReadStatus(&r), Fetching: r.flag(), Probed: r.flag(), RuleOK: r.flag(), Active: r.flag()}
	return v, serviceWireDone(&r.quotaReader)
}
func encodeServiceChinaCommand(v serviceChinaCommand) (string, error) {
	if v.Action < 1 || v.Action > 5 {
		return "", errors.New("china action")
	}
	w := servicePathWriter{b: []byte{v.Action}}
	chinaWireStatus(&w, v.Status)
	w.flag(v.Flag)
	return chinaWireFinish(&w)
}
func decodeServiceChinaCommand(b string) (serviceChinaCommand, error) {
	if b == "" || len(b) > serviceChinaMaxBytes {
		return serviceChinaCommand{}, errors.New("china size limit")
	}
	r := servicePathReader{quotaReader{b: []byte(b)}}
	v := serviceChinaCommand{Action: r.byte(), Status: chinaReadStatus(&r), Flag: r.flag()}
	if v.Action < 1 || v.Action > 5 {
		return v, errors.New("china action")
	}
	return v, serviceWireDone(&r.quotaReader)
}
