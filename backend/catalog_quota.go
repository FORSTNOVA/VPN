package main

import "encoding/binary"

type quotaCommands interface {
	graphCommands
	release()
}

func (s *serviceAuthority) writeQuota(h uint64, p *subscriptionUserInfo) {
	var b [32]byte
	for i, v := range [...]int64{p.Upload, p.Download, p.Total, p.Expire} {
		binary.LittleEndian.PutUint64(b[i*8:], uint64(v))
	}
	s.quotaStore.call(1, uintptr(h), b[:])
}

func (s *serviceAuthority) quotaSnapshot(h uint64, detached bool) *subscriptionUserInfo {
	if h == 0 {
		return nil
	}
	p, ok := s.infos[h]
	if !ok || (p == nil && s.quotaStore == nil) {
		s.catalogFault = errMissingQuota
		panic(s.catalogFault)
	}
	if s.quotaStore == nil {
		if !detached {
			return p
		}
		v := *p
		return &v
	}
	var b [32]byte
	s.quotaStore.call(2, uintptr(h), b[:])
	word := func(i int) int64 { return int64(binary.LittleEndian.Uint64(b[i:])) }
	return &subscriptionUserInfo{word(0), word(8), word(16), word(24)}
}

func (s *serviceAuthority) registerFetchedQuota(p *subscriptionUserInfo) uint64 {
	v := *p
	return s.registerQuota(&v)
}

func (s *serviceAuthority) retireQuota(h uint64) {
	if s.quotaStore != nil {
		s.quotaStore.call(3, uintptr(h), nil)
	}
	delete(s.infoReverse, s.infos[h])
	delete(s.infos, h)
}
