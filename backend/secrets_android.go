//go:build android

package main

import (
	"errors"
	"strings"
)

// sealedPrefix marks a value this platform wrote. Anything without it is a
// value that came from somewhere else — a profile carried over from the Windows
// copy, or one written before this app used the keystore — and is passed
// through unchanged, so an existing profile keeps working instead of failing to
// load.
const sealedPrefix = "enc:v1:"

// sealSecret puts a subscription URL away in a form only this app on this
// device can read.
//
// The Android keystore holds a key that this process can use and cannot read:
// it is derived and kept by the system, and on a device with a secure element
// it never enters the app's memory at all. The URL — which is a credential, and
// the only one this app has — is therefore not in the file in the clear, and a
// copy of the profile taken off the device is not a copy of the subscription.
//
// What this does not defend against is the device itself: an app running as
// this user on an unlocked device can ask for the same decryption. It defends
// the file.
func sealSecret(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if strings.HasPrefix(plain, sealedPrefix) {
		// Already sealed; sealing it twice would only add a layer that a later
		// version would have to know to remove.
		return plain, nil
	}
	blob, err := jniSeal(plain)
	if err != nil {
		return "", err
	}
	if blob == "" {
		return "", errors.New("the keystore returned nothing")
	}
	// The prefix is written here rather than by the keystore: it belongs to the
	// stored form, which is this file's business.
	return sealedPrefix + blob, nil
}

func openSecret(stored string) (string, error) {
	if !strings.HasPrefix(stored, sealedPrefix) {
		return stored, nil
	}
	return jniOpen(strings.TrimPrefix(stored, sealedPrefix))
}
