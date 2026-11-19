import AppKit
import CoreGraphics
import Foundation
import Vision

if CommandLine.arguments.count > 1 && CommandLine.arguments[1] == "--front-window" {
  let skip = CommandLine.arguments.count > 2 ? CommandLine.arguments[2].lowercased() : ""
  let opts = CGWindowListOption(arrayLiteral: .optionOnScreenOnly, .excludeDesktopElements)
  guard let list = CGWindowListCopyWindowInfo(opts, kCGNullWindowID) as? [[String: Any]] else {
    fputs("没有窗口列表\n", stderr)
    exit(1)
  }
  for w in list {
    let layer = (w[kCGWindowLayer as String] as? NSNumber)?.intValue ?? 0
    if layer != 0 { continue }
    let owner = ((w[kCGWindowOwnerName as String] as? String) ?? "").lowercased()
    if owner.contains("multi-agent") || owner.contains("agent desktop") || owner.contains("舰队") {
      continue
    }
    if skip != "" && owner.contains(skip) { continue }
    let bounds = w[kCGWindowBounds as String] as? [String: Any]
    let width = (bounds?["Width"] as? NSNumber)?.doubleValue ?? 0
    let height = (bounds?["Height"] as? NSNumber)?.doubleValue ?? 0
    if width < 240 || height < 180 { continue }
    let id = (w[kCGWindowNumber as String] as? NSNumber)?.intValue ?? 0
    if id > 0 {
      print(id)
      exit(0)
    }
  }
  fputs("没有前台窗口\n", stderr)
  exit(1)
}

let path = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : ""
guard let src = NSImage(contentsOfFile: path) else {
  fputs("打不开截图\n", stderr)
  exit(1)
}
let maxW: CGFloat = 1280
var size = src.size
if size.width > maxW && size.width > 0 {
  let scale = maxW / size.width
  size = NSSize(width: maxW, height: max(1, size.height * scale))
}
let img = NSImage(size: size)
img.lockFocus()
src.draw(in: NSRect(origin: .zero, size: size), from: NSRect(origin: .zero, size: src.size), operation: .copy, fraction: 1)
img.unlockFocus()
guard let tiff = img.tiffRepresentation,
      let rep = NSBitmapImageRep(data: tiff),
      let cg = rep.cgImage else {
  fputs("无法解码截图\n", stderr)
  exit(1)
}
let req = VNRecognizeTextRequest()
req.recognitionLevel = .fast
req.usesLanguageCorrection = false
req.recognitionLanguages = ["zh-Hans", "en-US"]
let handler = VNImageRequestHandler(cgImage: cg, options: [:])
do {
  try handler.perform([req])
} catch {
  fputs("\(error)\n", stderr)
  exit(1)
}
var lines: [String] = []
for obs in req.results ?? [] {
  if let s = obs.topCandidates(1).first?.string {
    lines.append(s)
  }
}
var text = lines.joined(separator: "\n")
if text.count > 4000 {
  text = String(text.prefix(4000))
}
print(text)
