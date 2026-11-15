package nativepermissions

// Desktop Windows builds are unpackaged Win32 applications. Linux distribution
// does not assume a particular desktop, sandbox or portal implementation.
func systemItem(platform string, id ID) Item {
	item := Item{ID: id, State: "system", Detail: "linux-system"}
	if platform == "windows" {
		item.Detail = "windows-system"
		item.Settings = settingsURL(platform, id) != ""
		if id == FullDisk || id == Accessibility || id == Screen || id == Input || id == Automation {
			item.State, item.Detail = "not-applicable", "windows-no-tcc"
		}
	} else if platform == "darwin" {
		item.State, item.Detail, item.Settings = "unavailable", "native-unavailable", true
	}
	return item
}
