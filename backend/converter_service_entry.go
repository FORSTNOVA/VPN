package main

import "fmt"

func runConverterService(q converterServiceRequest, e *appConverterEffects) (r converterServiceResult) {
	defer func() {
		if v := recover(); v != nil {
			r = converterServiceResult{Err: fmt.Errorf("converter service interrupted: %v", v)}
		}
	}()
	return runSelectedConverterService(q, e)
}
func converterBodyRequest(op string, body []byte) (converterServiceRequest, *appConverterEffects) {
	e := newConverterEffects()
	return converterServiceRequest{Op: op, Body: e.put(body)}, e
}
func converterProxyResult(e *appConverterEffects, r converterServiceResult) ([]map[string]any, []proxyNode, error) {
	var p []map[string]any
	var n []proxyNode
	if r.Err != nil {
		return nil, nil, r.Err
	}
	if r.Proxies != 0 {
		p = e.get(r.Proxies).([]map[string]any)
	}
	if r.Nodes != 0 {
		n = e.get(r.Nodes).([]proxyNode)
	}
	return p, n, nil
}
func converterByteResult(e *appConverterEffects, r converterServiceResult) ([]byte, []proxyNode, error) {
	if r.Err != nil {
		return nil, nil, r.Err
	}
	var b []byte
	var n []proxyNode
	if r.Body != 0 {
		b = e.bytes(r.Body)
	}
	if r.Nodes != 0 {
		n = e.get(r.Nodes).([]proxyNode)
	}
	return b, n, nil
}
