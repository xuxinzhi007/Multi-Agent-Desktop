import AppKit
import Foundation
import Vision

let path = CommandLine.arguments[1]
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
