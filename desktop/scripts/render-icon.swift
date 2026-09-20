// macOS: swift scripts/render-icon.swift assets/appicon.svg build/appicon.png
import AppKit
import Foundation
let args = CommandLine.arguments
 guard args.count == 3 else { fatalError("usage: render-icon.swift input.svg output.png") }
let data = try Data(contentsOf: URL(fileURLWithPath: args[1]))
guard let image = NSImage(data: data) else { fatalError("Cannot load SVG") }
let size = 1024
let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size,
    bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
    colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
image.draw(in: NSRect(x: 0, y: 0, width: size, height: size), from: .zero, operation: .copy, fraction: 1)
NSGraphicsContext.restoreGraphicsState()
guard let png = bitmap.representation(using: .png, properties: [:]) else { fatalError("Cannot encode PNG") }
try png.write(to: URL(fileURLWithPath: args[2]))
print("Rendered 1024px RGBA app icon")
