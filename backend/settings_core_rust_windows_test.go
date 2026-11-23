//go:build windows && amd64 && rustcore

package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRustSettingsParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_SETTINGS_RUST") == "1"
	if settingsRustAvailable() != expected {
		t.Fatal("settings capability mismatch")
	}
	inputs := []string{"", " ", "game.exe", "GAME.EXE", "游戏.exe", "².exe", "a,b.exe", ".exe", "../game.exe", `C:\Games\x.exe`, `C:/Games/../a,b.exe`, `\\server\share\x.exe`, "C:game.exe", "/game.exe", "a.exe\n", "a\x00.exe", "foo?.exe", "com.game.app", "a.b", "a.1", "A.b_1", "com", "com..app", "com.app-1", "com.app\n", "\xff.exe"}
	for _, s := range inputs {
		if windowsGameRule(s) != windowsGameRuleGo(s) {
			t.Fatalf("rule differs for %q", s)
		}
	}
	for _, android := range []bool{false, true} {
		for _, a := range inputs {
			for _, b := range inputs {
				values := []string{" ", a, b, a, strings.ToUpper(a)}
				want, we := normalizeGameBypassGo(values, android)
				got, ge := normalizeGameBypass(values, android)
				if !reflect.DeepEqual(want, got) || (we == nil) != (ge == nil) || (we != nil && we.Error() != ge.Error()) {
					t.Fatalf("normalize mismatch android=%v inputs=%q got=%q err=%v want=%q err=%v", android, values, got, ge, want, we)
				}
			}
		}
	}
	for _, port := range []int{-9223372036854775807, -1, 0, 1, 65535, 65536, 9223372036854775807} {
		for _, windows := range []bool{false, true} {
			if validSocksSetting(port, windows) != validSocksSettingGo(port, windows) {
				t.Fatal("port differs")
			}
		}
	}
	if expected {
		for _, op := range []uint32{1, 2, 3} {
			v := []string{}
			if op == 1 {
				v = []string{"game.exe", "game.exe", "1", "0"}
			}
			if _, _, ok := rustSettingsCall(op, v); !ok {
				t.Fatalf("op %d unexpectedly fallback", op)
			}
		}
	}
}
