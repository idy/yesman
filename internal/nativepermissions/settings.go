package nativepermissions

// These are fixed OS-owned destinations, never URLs or commands from the page.
// System audio has no anchor of its own: its System Audio Recording Only list
// is the lower part of the Screen & System Audio Recording pane.
func settingsURL(platform string, id ID) string {
	if platform == "darwin" {
		anchors := map[ID]string{Bluetooth: "Privacy_Bluetooth", FullDisk: "Privacy_AllFiles", Microphone: "Privacy_Microphone", Camera: "Privacy_Camera", Accessibility: "Privacy_Accessibility", Screen: "Privacy_ScreenCapture", SystemAudio: "Privacy_ScreenCapture", Input: "Privacy_ListenEvent", Automation: "Privacy_Automation"}
		if id == Notifications {
			return "x-apple.systempreferences:com.apple.preference.notifications"
		}
		if anchor := anchors[id]; anchor != "" {
			return "x-apple.systempreferences:com.apple.preference.security?" + anchor
		}
	}
	if platform == "windows" {
		return map[ID]string{Bluetooth: "ms-settings:bluetooth", Notifications: "ms-settings:notifications", Microphone: "ms-settings:privacy-microphone", Camera: "ms-settings:privacy-webcam"}[id]
	}
	return ""
}
