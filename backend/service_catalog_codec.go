package main

import "errors"

const serviceCatalogMaxBytes = 16 << 20
const serviceCatalogMaxItems = 100000

type serviceCatalogWriter struct{ serviceHealthWriter }
type serviceCatalogReader struct{ serviceHealthReader }

func (w *serviceCatalogWriter) entry(v serviceCatalogEntry) {
	w.str(v.ID)
	w.str(v.URL)
	w.str(v.Label)
	w.str(v.Prefix)
	w.flag(v.Enabled)
	w.n(uint64(v.UpdatedAt))
	w.n(uint64(v.NodeCount))
	w.str(v.LastError)
	w.n(v.InfoHandle)
	w.n(uint64(v.QuotaUpdatedAt))
	w.str(v.QuotaError)
}
func (r *serviceCatalogReader) entry() serviceCatalogEntry {
	return serviceCatalogEntry{ID: r.str(), URL: r.str(), Label: r.str(), Prefix: r.str(), Enabled: r.flag(), UpdatedAt: r.integer(), NodeCount: r.platformInt(), LastError: r.str(), InfoHandle: r.n(), QuotaUpdatedAt: r.integer(), QuotaError: r.str()}
}
func (w *serviceCatalogWriter) entries(v []serviceCatalogEntry) {
	w.count(v != nil, len(v))
	for _, e := range v {
		w.entry(e)
	}
}
func (r *serviceCatalogReader) entries() []serviceCatalogEntry {
	p, n := r.count()
	if !p {
		return nil
	}
	v := make([]serviceCatalogEntry, n)
	for i := range v {
		v[i] = r.entry()
	}
	return v
}
func (w *serviceCatalogWriter) state(v serviceCatalogState) {
	w.entries(v.Entries)
	w.flag(v.FromCache)
	w.flag(v.HasBackup)
	w.entries(v.Backup)
}
func (r *serviceCatalogReader) state() serviceCatalogState {
	return serviceCatalogState{Entries: r.entries(), FromCache: r.flag(), HasBackup: r.flag(), Backup: r.entries()}
}
func catalogReader(b string) (serviceCatalogReader, error) {
	if b == "" || len(b) > serviceCatalogMaxBytes {
		return serviceCatalogReader{}, errors.New("invalid catalog encoding size")
	}
	return serviceCatalogReader{serviceHealthReader{quotaReader{b: []byte(b)}}}, nil
}
func encodeServiceCatalogState(v serviceCatalogState) (string, error) {
	w := serviceCatalogWriter{}
	w.state(v)
	return w.finish()
}
func decodeServiceCatalogState(b string) (serviceCatalogState, error) {
	if b == "" {
		return serviceCatalogState{}, nil
	}
	r, e := catalogReader(b)
	if e != nil {
		return serviceCatalogState{}, e
	}
	v := r.state()
	return v, serviceWireDone(&r.quotaReader)
}
func encodeServiceCatalogCommand(v serviceCatalogCommand) (string, error) {
	w := serviceCatalogWriter{serviceHealthWriter: serviceHealthWriter{b: []byte{v.Action}}}
	w.n(uint64(v.Index))
	w.n(v.Mask)
	w.entry(v.Entry)
	w.entries(v.Entries)
	w.str(v.ID)
	w.str(v.URL)
	w.flag(v.Flag)
	return w.finish()
}
func decodeServiceCatalogCommand(b string) (serviceCatalogCommand, error) {
	r, e := catalogReader(b)
	if e != nil {
		return serviceCatalogCommand{}, e
	}
	v := serviceCatalogCommand{Action: r.byte(), Index: r.platformInt(), Mask: r.n(), Entry: r.entry(), Entries: r.entries(), ID: r.str(), URL: r.str(), Flag: r.flag()}
	return v, serviceWireDone(&r.quotaReader)
}
func encodeServiceCatalogQuery(v serviceCatalogQuery) (string, error) {
	w := serviceCatalogWriter{serviceHealthWriter: serviceHealthWriter{b: []byte{v.Kind}}}
	w.n(uint64(v.Index))
	w.str(v.ID)
	w.str(v.URL)
	w.n(v.Handle)
	return w.finish()
}
func decodeServiceCatalogQuery(b string) (serviceCatalogQuery, error) {
	r, e := catalogReader(b)
	if e != nil {
		return serviceCatalogQuery{}, e
	}
	v := serviceCatalogQuery{Kind: r.byte(), Index: r.platformInt(), ID: r.str(), URL: r.str(), Handle: r.n()}
	return v, serviceWireDone(&r.quotaReader)
}
func encodeServiceCatalogReply(v serviceCatalogReply) (string, error) {
	w := serviceCatalogWriter{}
	w.flag(v.Found)
	w.n(uint64(v.Count))
	w.flag(v.NilEntries)
	w.flag(v.FromCache)
	w.flag(v.HasBackup)
	w.entry(v.Entry)
	w.entries(v.Entries)
	w.n(v.References)
	return w.finish()
}
func decodeServiceCatalogReply(b string) (serviceCatalogReply, error) {
	r, e := catalogReader(b)
	if e != nil {
		return serviceCatalogReply{}, e
	}
	v := serviceCatalogReply{Found: r.flag(), Count: r.platformInt(), NilEntries: r.flag(), FromCache: r.flag(), HasBackup: r.flag(), Entry: r.entry(), Entries: r.entries(), References: r.n()}
	return v, serviceWireDone(&r.quotaReader)
}
