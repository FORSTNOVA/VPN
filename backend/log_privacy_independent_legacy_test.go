package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLogPrivacyIndependentLegacySecrets(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	short := "abcdefghijkl"
	long := short + "long"
	invalid := string([]byte{255}) + "012345678901"
	testSetSettings(a, settings{SubscriptionURL: " \t" + short + "\n"})
	testCatalogWrite(a, []subscriptionEntry{{URL: long}, {URL: short}, {URL: "12345678901"}, {URL: "中文中文"}, {URL: invalid}, {URL: "\u2003" + long + "\u2003"}})
	want := []string{short, long, "中文中文", invalid}
	if got := a.subscriptionSecrets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("filter/order bytes %#v", got)
	}
}
func TestLogPrivacyIndependentLegacyOverlapBytes(t *testing.T) {
	a := settingsDocumentTestApp(t, t.TempDir())
	short := "abcdefghijkl"
	long := short + "long"
	testSetSettings(a, settings{SubscriptionURL: short})
	testCatalogWrite(a, []subscriptionEntry{{URL: long}})
	path := filepath.Join(a.home, "mihomo.log")
	body := append([]byte{255, 0}, []byte(long+" "+short+"\n")...)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	a.scrubKernelLog()
	got, err := os.ReadFile(path)
	want := append([]byte{255, 0}, []byte("<redacted>long <redacted>\n")...)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("ordered overlap %q %v", got, err)
	}
	if _, err = os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("successful rename must consume tmp")
	}
}
func TestLogPrivacyIndependentLegacyTail(t *testing.T) {
	body := []byte("head\n0123456789\nend")
	got := tailFromLineGo(body, 5)
	if string(got) != "0123456789\nend" {
		t.Fatalf("line backup %q", got)
	}
	got[0] = 'X'
	if body[5] != 'X' {
		t.Fatal("tail original backing lost")
	}
	if string(tailFromLineGo([]byte("abcdefgh"), 3)) != "fgh" {
		t.Fatal("no-line hard tail")
	}
}
