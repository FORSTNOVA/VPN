package main

import (
	"bytes"
	"fmt"
	"testing"
	"testing/iotest"
)

func TestRelayEndpointStreamParity(t *testing.T) {
	cases := [][]byte{
		{5, 0, 0, 1, 0, 0, 0, 0, 0, 0},
		{5, 0, 0xff, 1, 192, 0, 2, 1, 0xff, 0xff},
		{5, 0, 0, 3, 0, 0, 53}, {5, 0, 0, 3, 2, ':', ':', 0, 80},
		{5, 0, 0, 3, 3, 0xff, 0, ':', 0, 80},
		{5, 0, 0, 7}, {4, 1, 0, 9},
	}
	for _, addr := range [][]byte{
		make([]byte, 16),
		{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255, 192, 0, 2, 1},
		{0x20, 1, 0xd, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
	} {
		cases = append(cases, append(append([]byte{5, 0, 0, 4}, addr...), 0, 53))
	}
	cases = append(cases, append(append([]byte{5, 0, 0, 3, 255}, bytes.Repeat([]byte{':'}, 255)...), 255, 255))
	for code := 1; code <= 255; code++ {
		cases = append(cases, []byte{5, byte(code), 0, 7})
	}
	for _, packet := range cases {
		for end := 0; end <= len(packet); end++ {
			stream := bytes.NewReader(packet[:end])
			got, err := readRelayEndpoint(iotest.OneByteReader(stream))
			oracle := bytes.NewReader(packet[:end])
			want, wantErr := readRelayEndpointGo(oracle)
			if got != want || fmt.Sprint(err) != fmt.Sprint(wantErr) || stream.Len() != oracle.Len() {
				t.Fatalf("reply %x: %q %v, want %q %v", packet[:end], got, err, want, wantErr)
			}
		}
		withTail := bytes.NewReader(append(append([]byte{}, packet...), 0xaa))
		_, _ = readRelayEndpoint(withTail)
		if withTail.Len() != 1 {
			t.Fatalf("read beyond reply %x", packet)
		}
	}
}
