package safefs

import (
	"testing"

	"golang.org/x/sys/windows"
)

// Apart from the all-zero "no creation time", raw FILETIMEs convert exactly
// as Filetime.Nanoseconds, which recorded identities were made with, so
// existing records still match.
func TestSafeFSFiletimeMatchesWindowsConversion(t *testing.T) {
	for _, ft := range []windows.Filetime{
		{LowDateTime: 0xD53E8000, HighDateTime: 0x019DB1DE}, // the Unix epoch
		{LowDateTime: 0xD53E8001, HighDateTime: 0x019DB1DE},
		{LowDateTime: 0x12345678, HighDateTime: 0x01DC5A00},
		{LowDateTime: 0, HighDateTime: 0x01000000},
	} {
		got, ok := birthFromFiletime(ft.LowDateTime, ft.HighDateTime)
		if want := ft.Nanoseconds(); !ok || got != want {
			t.Errorf("%+v: %d %v, want %d true", ft, got, ok, want)
		}
	}
	if _, ok := birthFromFiletime(0, 0); ok {
		t.Error("an all-zero FILETIME reports a creation time")
	}
}
