package main

import (
	"errors"
	"time"
)

type serviceChinaState struct {
	Status                           chinaIPStatus
	Fetching, Probed, RuleOK, Active bool
}
type serviceChinaCommand struct {
	Action uint8
	Status chinaIPStatus
	Flag   bool
}

func applyServiceChina(s *serviceChinaState, c serviceChinaCommand) error {
	if c.Status.Refreshing || c.Status.Active {
		return errors.New("invalid china status flags")
	}
	v := *s
	switch c.Action {
	case 1:
		v.Status = c.Status
	case 2:
		v.Fetching = c.Flag
	case 3:
		v.Probed = c.Flag
	case 4:
		v.RuleOK = c.Flag
	case 5:
		v.Active = c.Flag
	default:
		return errors.New("invalid china action")
	}
	if _, e := encodeServiceChinaState(v); e != nil {
		return e
	}
	*s = v
	return nil
}
func (a *app) chinaStateLocked() serviceChinaState {
	s := a.serviceStateLocked()
	if s.closed || s.pending != nil || s.catalogFault != nil {
		panic("china authority unavailable")
	}
	v, e := s.backend.ChinaQuery()
	if e != nil {
		panic(e)
	}
	h, e := decodeServiceChinaState(v)
	if e != nil {
		panic(e)
	}
	return h
}
func (a *app) chinaCommandLocked(c serviceChinaCommand) {
	p, e := encodeServiceChinaCommand(c)
	if e != nil {
		panic(e)
	}
	if e = a.serviceStateLocked().mutate(serviceStateCommand{Kind: 7, ChinaPayload: p}); e != nil {
		panic(e)
	}
}
func (a *app) setChinaActiveLocked(v bool) {
	a.chinaCommandLocked(serviceChinaCommand{Action: 5, Flag: v})
}

// Opaque bodies/configs and OS resources stay in the invocation executor.
type chinaServiceRequest struct {
	Op              string
	Mode            chinaListMode
	Body            []byte
	Ranges          []string
	HasSubscription bool
	Fetch           func() ([]byte, string, error)
}
type chinaServiceEffect struct {
	Kind       string
	Index      int
	Name, Text string
	Body       []byte
	Ranges     []string
	Status     chinaIPStatus
	Flag       bool
	Action     uint8
	Time       time.Time
}
type chinaServiceReply struct {
	Err          error
	Text         string
	Body         []byte
	Ranges       []string
	Names        []string
	Now          time.Time
	Flag         bool
	Count        int
	State        serviceChinaState
	Options      configOptions
	ImportPolicy bool
}
type chinaServiceResult struct {
	Now     time.Time
	Err     error
	Body    []byte
	Source  string
	Ranges  []string
	Status  chinaIPStatus
	Flag    bool
	Options configOptions
}
type chinaServiceEffects interface {
	Call(chinaServiceEffect) chinaServiceReply
	Release()
}

func (b *goServiceStateBackend) ChinaQuery() (string, error) {
	if b.closed {
		return "", errServiceStateClosed
	}
	return b.state.Snapshot.ChinaPayload, nil
}
