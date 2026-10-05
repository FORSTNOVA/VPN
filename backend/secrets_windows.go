//go:build windows

package main

// Windows keeps a subscription URL as it is written.
//
// There is a tempting argument for encrypting it here and no real one: the
// service has to fetch the subscription, so it has to be able to read the URL
// on its own, which means the key would have to be readable by anything running
// as this user as well. That is not a secret, it is an obfuscation, and it
// would buy nothing while making a profile that a user wants to inspect by hand
// unreadable. What this app does defend against on Windows is the log, and that
// is the redactor's job. The profile itself is protected by the file system,
// with the settings file readable only by its owner.
//
// Android is different, and the code there explains why: the system offers a
// key that this app can use but not read, so the file stops being enough on its
// own for anyone who copies it off the device.

func sealSecret(plain string) (string, error) { return plain, nil }

func openSecret(stored string) (string, error) { return stored, nil }
