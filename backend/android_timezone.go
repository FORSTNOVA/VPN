//go:build android

package main

/*
#include <time.h>
*/
import "C"

import "time"

// init puts this process's clock in the device's time zone.
//
// Go reads the zone from TZ or /etc/localtime, and an Android app has neither:
// the setting lives in a system property that only the C library consults. Every
// timestamp this service writes would otherwise be UTC while the phone shows
// local time — and the kernel's log, which lands beside ours, would agree with
// the phone rather than with the service it sits next to.
//
// The trick is the one the kernel's own Android build uses: ask the C library
// what the local offset is right now, and pin Go to that.
func init() {
	var now C.time_t
	var broken C.struct_tm
	C.time(&now)
	C.localtime_r(&now, &broken)
	time.Local = time.FixedZone(C.GoString(broken.tm_zone), int(broken.tm_gmtoff))
}
