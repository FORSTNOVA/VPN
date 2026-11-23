package main

import (
	"encoding/binary"
	"maps"
	"math"
	"slices"
)

func decodeRustNodeValue(data []byte) (map[string]any, bool) {
	return decodeNodeObject(data, false)
}
func decodeNodeObject(data []byte, full bool) (map[string]any, bool) {
	offset := 0
	readN := func() (uint64, bool) {
		if len(data)-offset < 8 {
			return 0, false
		}
		n := binary.LittleEndian.Uint64(data[offset:])
		offset += 8
		return n, true
	}
	readString := func() (string, bool) {
		n, ok := readN()
		if !ok || n > uint64(len(data)-offset) {
			return "", false
		}
		s := string(data[offset : offset+int(n)])
		offset += int(n)
		return s, true
	}
	var read func(int) (any, bool)
	read = func(depth int) (any, bool) {
		if depth > 16 || offset >= len(data) {
			return nil, false
		}
		tag := data[offset]
		offset++
		if !full && (tag < 1 || tag > 5) {
			return nil, false
		}
		switch tag {
		case 1:
			return readString()
		case 2:
			if offset >= len(data) || data[offset] > 1 {
				return nil, false
			}
			b := data[offset] == 1
			offset++
			return b, true
		case 3:
			n, ok := readN()
			return int(int64(n)), ok
		case 0:
			return nil, true
		case 6:
			bits, ok := readN()
			return math.Float64frombits(bits), ok
		case 7:
			n, ok := readN()
			if !ok || n > uint64(len(data)-offset) {
				return nil, false
			}
			a := make([]any, int(n))
			for i := range a {
				a[i], ok = read(depth + 1)
				if !ok {
					return nil, false
				}
			}
			return a, true
		case 8:
			n, ok := readN()
			if !ok || n > uint64((len(data)-offset)/16) {
				return nil, false
			}
			m := make(map[string]string, int(n))
			for i := uint64(0); i < n; i++ {
				k, ok := readString()
				if !ok {
					return nil, false
				}
				v, ok := readString()
				if !ok {
					return nil, false
				}
				m[k] = v
			}
			return m, true
		case 9:
			return map[string]any(nil), true
		case 10:
			return []any(nil), true
		case 11:
			return []string(nil), true
		case 12:
			return map[string]string(nil), true
		case 4:
			n, ok := readN()
			if !ok || n > uint64(len(data)-offset) {
				return nil, false
			}
			values := make([]string, 0, int(n))
			for i := uint64(0); i < n; i++ {
				v, ok := read(depth + 1)
				s, isString := v.(string)
				if !ok || !isString {
					return nil, false
				}
				values = append(values, s)
			}
			return values, true
		case 5:
			n, ok := readN()
			if !ok || n > uint64((len(data)-offset)/9) {
				return nil, false
			}
			m := map[string]any{}
			for i := uint64(0); i < n; i++ {
				k, ok := readString()
				if !ok {
					return nil, false
				}
				v, ok := read(depth + 1)
				if !ok {
					return nil, false
				}
				m[k] = v
			}
			return m, true
		default:
			return nil, false
		}
	}
	value, ok := read(0)
	m, isMap := value.(map[string]any)
	return m, ok && isMap && offset == len(data)
}

// Tags 1–5 are shared with the node-fields ABI. The additional tags preserve
// SQLite JSON numbers, list element types and typed nil containers on reload.
func encodeManualValue(b []byte, v any, depth int) ([]byte, bool) {
	if depth > 16 {
		return nil, false
	}
	switch x := v.(type) {
	case nil:
		return append(b, 0), true
	case string:
		return registryBytes(append(b, 1), x), true
	case bool:
		return registryBool(append(b, 2), x), true
	case int:
		return registryInt(append(b, 3), int64(x)), true
	case float64:
		return binary.LittleEndian.AppendUint64(append(b, 6), math.Float64bits(x)), true
	case []string:
		if x == nil {
			return append(b, 11), true
		}
		b = registryInt(append(b, 4), int64(len(x)))
		for _, s := range x {
			if depth == 16 {
				return nil, false
			}
			b = registryBytes(append(b, 1), s)
		}
	case []any:
		if x == nil {
			return append(b, 10), true
		}
		b = registryInt(append(b, 7), int64(len(x)))
		for _, v := range x {
			var ok bool
			b, ok = encodeManualValue(b, v, depth+1)
			if !ok {
				return nil, false
			}
		}
	case map[string]any:
		if x == nil {
			return append(b, 9), true
		}
		b = registryInt(append(b, 5), int64(len(x)))
		for _, k := range slices.Sorted(maps.Keys(x)) {
			v := x[k]
			b = registryBytes(b, k)
			var ok bool
			b, ok = encodeManualValue(b, v, depth+1)
			if !ok {
				return nil, false
			}
		}
	case map[string]string:
		if x == nil {
			return append(b, 12), true
		}
		b = registryInt(append(b, 8), int64(len(x)))
		for _, k := range slices.Sorted(maps.Keys(x)) {
			v := x[k]
			b = registryBytes(b, k)
			b = registryBytes(b, v)
		}
	default:
		return nil, false
	}
	return b, true
}
