import ApplicationServices
import CoreGraphics
import Foundation

print("screen \(CGPreflightScreenCaptureAccess() ? "yes" : "no")")
print("accessibility \(AXIsProcessTrusted() ? "yes" : "no")")

func automation(_ bundle: String) -> String {
    let bytes = Array(bundle.utf8)
    var target = AEAddressDesc()
    let created = bytes.withUnsafeBytes { raw -> OSStatus in
        guard let base = raw.baseAddress else { return OSStatus(-1) }
        return OSStatus(AECreateDesc(DescType(typeApplicationBundleID), base, raw.count, &target))
    }
    if created != noErr {
        return "unknown"
    }
    defer { AEDisposeDesc(&target) }
    let status = AEDeterminePermissionToAutomateTarget(
        &target,
        AEEventClass(typeWildCard),
        AEEventID(typeWildCard),
        false
    )
    switch status {
    case noErr:
        return "yes"
    case OSStatus(-1743), OSStatus(-1744):
        return "no"
    default:
        return "unknown"
    }
}

let chrome = automation("com.google.Chrome")
let safari = automation("com.apple.Safari")
let auto: String
if chrome == "yes" || safari == "yes" {
    auto = "yes"
} else if chrome == "no" || safari == "no" {
    auto = "no"
} else {
    auto = "unknown"
}
print("automation \(auto)")
