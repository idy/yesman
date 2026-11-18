// Read-only process/application/window metadata for the hosted naming smoke.
// Uses public AppKit/CoreGraphics APIs; no TCC-protected inspection or capture.
import Foundation
import AppKit
import CoreGraphics
let pid = pid_t(CommandLine.arguments[1])!
let app = NSRunningApplication(processIdentifier: pid)
let info = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
let windows = info.filter { ($0[kCGWindowOwnerPID as String] as? Int) == Int(pid) && ($0[kCGWindowLayer as String] as? Int) == 0 }.map { w in
    ["bounds": w[kCGWindowBounds as String] ?? [:], "onScreen": w[kCGWindowIsOnscreen as String] ?? false] as [String: Any]
}
let result: [String: Any] = ["localizedName": app?.localizedName ?? "", "fileDisplayName": app?.bundleURL.map { FileManager.default.displayName(atPath: $0.path) } ?? "", "bundleIdentifier": app?.bundleIdentifier ?? "", "executable": app?.executableURL?.path ?? "", "windows": windows]
print(String(data: try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys]), encoding: .utf8)!)
