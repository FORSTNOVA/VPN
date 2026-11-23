package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
)

// Activation uses the live profile codec and cache fallback while holding the
// app lock. Keep the previous active files until both fetch and settings save
// succeed. Health and region deletes can wait until the new profile is live.
type priorFile struct {
	path    string
	data    []byte
	existed bool
}
type activationEffects struct {
	appSubscriptionEffects
	previous                           settingsServiceState
	control                            serviceControlState
	files                              []priorFile
	started, clearHealth, clearRegions bool
	err                                error
}

func (e *activationEffects) Call(kind, arg string, s *subscriptionServiceState) subscriptionEffectReply {
	if kind == "remove-nodes" && !e.started {
		a := e.a
		e.previous = settingsStateFromApp(a)
		e.control = a.controlMetaLocked()
		profile := a.profilePath()
		paths := []string{a.cachedNodesPath(), profile, profile + ".source", profile + ".format", filepath.Join(a.home, "settings.json")}
		if entry, ok := a.findSubscriptionByURL(s.Settings.SubscriptionURL); ok {
			cache := a.profileCachePath(entry.ID)
			paths = append(paths, cache, cache+".format")
		}
		e.files, e.err = captureFiles(paths...)
		e.started = true
	}
	if e.err != nil {
		return subscriptionEffectReply{Err: e.err}
	}
	switch kind {
	case "clear-health":
		e.clearHealth = true
		return subscriptionEffectReply{}
	case "clear-regions":
		e.clearRegions = true
		return subscriptionEffectReply{}
	}
	reply := e.appSubscriptionEffects.Call(kind, arg, s)
	if kind == "save-settings" && reply.Err != nil {
		e.err = reply.Err
	}
	return reply
}
func (e *activationEffects) finish(success bool) error {
	if !e.started {
		return nil
	}
	a := e.a
	if success && e.err == nil {
		if e.clearHealth {
			if err := a.store.clearHealth(); err != nil {
				log.Printf("subscription activation cleanup: %v", err)
			}
		}
		if e.clearRegions {
			if err := a.store.clearRegions(); err != nil {
				log.Printf("subscription activation cleanup: %v", err)
			}
		}
		return nil
	}
	a.patchControlLocked(controlSubscriptionURL|settingsClearPinsMask, e.control)
	a.replaceCachedNodesLocked(e.previous.CachedNodes, false)
	a.restoreHealthRecordsLocked(e.previous.Health)
	a.restoreRegionRecordsLocked(e.previous.Regions)
	a.setHealthBlockedLocked(e.previous.Blocked)
	a.setHealthReasonLocked(e.previous.BlockReason)
	a.setProfileFromCacheLocked(e.previous.ProfileFromCache)
	restoreErr := restoreFiles(e.files)
	a.refreshSettingsRedactor()
	return errors.Join(e.err, restoreErr)
}

func captureFiles(paths ...string) ([]priorFile, error) {
	var files []priorFile
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		files = append(files, priorFile{path, data, err == nil})
	}
	return files, nil
}
func restoreFiles(files []priorFile) error {
	var restoreErr error
	for _, file := range files {
		current, err := os.ReadFile(file.path)
		if !file.existed {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			err = os.Remove(file.path)
		} else if err != nil || !bytes.Equal(current, file.data) {
			var tmp string
			tmp, err = stagePrivateFile(filepath.Dir(file.path), file.data)
			if err == nil {
				err = os.Rename(tmp, file.path)
				if err != nil {
					os.Remove(tmp)
				}
			}
		}
		restoreErr = errors.Join(restoreErr, err)
	}
	return restoreErr
}
