// Read-only window metadata for the process launched by the CI smoke script.
// No Accessibility, Apple Events, screenshot or screen-recording request API.
import Foundation
import CoreGraphics

let pid = Int(CommandLine.arguments[1])!
let info = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
let windows = info.filter { ($0[kCGWindowOwnerPID as String] as? Int) == pid && ($0[kCGWindowLayer as String] as? Int) == 0 }.map { window in
    ["bounds": window[kCGWindowBounds as String] ?? [:], "onScreen": window[kCGWindowIsOnscreen as String] ?? false, "id": window[kCGWindowNumber as String] ?? 0] as [String: Any]
}
let data = try JSONSerialization.data(withJSONObject: windows, options: [.sortedKeys])
print(String(data: data, encoding: .utf8)!)
