//go:build darwin

package main

import (
	"errors"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// readSavedLanguageTag reads AppleLanguages from the app's own
// CFPreferences domain. It uses the same current-application, current-user,
// any-host domain MyGo's App.SetLocale writes, without importing framework
// internals or writing preferences. Missing means System; any explicit tag,
// including one this app does not ship, stays explicit and is returned raw.
func readSavedLanguageTag() string {
	cf, err := loadCoreFoundation()
	if err != nil {
		return ""
	}
	key := cfString("AppleLanguages")
	if key == 0 {
		return ""
	}
	defer cf.release(key)
	cf.synchronize(cf.currentApplication, cf.currentUser, cf.anyHost)
	value := cf.copyValue(key, cf.currentApplication, cf.currentUser, cf.anyHost)
	if value == 0 {
		return ""
	}
	defer cf.release(value)
	if cf.getTypeID(value) != cf.arrayTypeID() {
		return ""
	}
	if cf.arrayCount(value) == 0 {
		return ""
	}
	item := cf.arrayValue(value, 0)
	if item == 0 || cf.getTypeID(item) != cf.stringTypeID() {
		return ""
	}
	var buf [256]byte
	if !cf.stringCString(item, &buf[0], int64(len(buf)), cfStringEncodingUTF8) {
		return ""
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return ""
}

const cfStringEncodingUTF8 = 0x08000100

type coreFoundation struct {
	handle uintptr

	copyValue     func(key, app, user, host uintptr) uintptr
	synchronize   func(app, user, host uintptr) bool
	getTypeID     func(value uintptr) uintptr
	arrayCount    func(value uintptr) int64
	arrayValue    func(value uintptr, index int64) uintptr
	arrayTypeID   func() uintptr
	stringTypeID  func() uintptr
	stringCString func(value uintptr, buf *byte, size int64, encoding uint32) bool
	createString  func(allocator uintptr, cstr *byte, encoding uint32) uintptr
	release       func(value uintptr)

	currentApplication uintptr
	currentUser        uintptr
	anyHost            uintptr
	dlsym              func(handle uintptr, name string) unsafe.Pointer
}

var (
	cfOnce sync.Once
	cf     coreFoundation
	cfErr  error
)

func loadCoreFoundation() (*coreFoundation, error) {
	cfOnce.Do(func() {
		h, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			cfErr = err
			return
		}
		system, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			cfErr = err
			return
		}
		purego.RegisterLibFunc(&cf.dlsym, system, "dlsym")
		cf.handle = h
		purego.RegisterLibFunc(&cf.copyValue, h, "CFPreferencesCopyValue")
		purego.RegisterLibFunc(&cf.synchronize, h, "CFPreferencesSynchronize")
		purego.RegisterLibFunc(&cf.getTypeID, h, "CFGetTypeID")
		purego.RegisterLibFunc(&cf.arrayCount, h, "CFArrayGetCount")
		purego.RegisterLibFunc(&cf.arrayValue, h, "CFArrayGetValueAtIndex")
		purego.RegisterLibFunc(&cf.arrayTypeID, h, "CFArrayGetTypeID")
		purego.RegisterLibFunc(&cf.stringTypeID, h, "CFStringGetTypeID")
		purego.RegisterLibFunc(&cf.stringCString, h, "CFStringGetCString")
		purego.RegisterLibFunc(&cf.createString, h, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&cf.release, h, "CFRelease")
		cf.currentApplication = cf.constant("kCFPreferencesCurrentApplication")
		cf.currentUser = cf.constant("kCFPreferencesCurrentUser")
		cf.anyHost = cf.constant("kCFPreferencesAnyHost")
		if cf.currentApplication == 0 || cf.currentUser == 0 || cf.anyHost == 0 {
			cfErr = errors.New("CoreFoundation preference domain constants unavailable")
		}
	})
	if cfErr != nil {
		return nil, cfErr
	}
	return &cf, nil
}

func (cf *coreFoundation) constant(name string) uintptr {
	if cf.dlsym == nil {
		return 0
	}
	p := cf.dlsym(cf.handle, name)
	if p == nil {
		return 0
	}
	return *(*uintptr)(p)
}

func cfString(s string) uintptr {
	bytes := append([]byte(s), 0)
	return cf.createString(0, &bytes[0], cfStringEncodingUTF8)
}
