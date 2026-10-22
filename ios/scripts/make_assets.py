#!/usr/bin/env python3
"""Builds the iOS asset catalog images from the design masters.

Usage: make_assets.py <design/assets dir> <ios/Meow/Resources/Assets.xcassets>

The app icon uses meow-logo-approved-blue-v2.png.
The supporting logos are re-derived from the opaque app icon master instead of the
transparent exports, which carry edge specks: the white head is separated
from the blue background by its red channel, which is ~8 on Meow blue and
255 on white.
"""
import json
import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw, ImageFilter

MEOW_BLUE = np.array([8, 121, 255], dtype=np.float32)
WHITE = np.array([255, 255, 255], dtype=np.float32)
INFO = {"author": "xcode", "version": 1}


def flood_from_border(solid):
    """Pixels not in `solid` that are connected to the image border."""
    h, w = solid.shape
    img = Image.new("L", (w + 2, h + 2), 0)
    img.paste(Image.fromarray(np.where(solid, 255, 0).astype(np.uint8)), (1, 1))
    ImageDraw.floodfill(img, (0, 0), 128)
    return np.asarray(img)[1:-1, 1:-1] == 128


def dilate(mask, size):
    img = Image.fromarray(np.where(mask, 255, 0).astype(np.uint8))
    return np.asarray(img.filter(ImageFilter.MaxFilter(size))) > 0


def save_rgba(rgb, alpha, path, width, pad=0.04):
    """Crops to the visible area, resizes with premultiplied alpha and saves."""
    rgba = np.dstack([np.clip(rgb, 0, 255), np.clip(alpha * 255, 0, 255)]).astype(np.uint8)
    ys, xs = np.nonzero(alpha > 0.02)
    p = int(max(np.ptp(xs), np.ptp(ys)) * pad)
    box = (max(xs.min() - p, 0), max(ys.min() - p, 0), min(xs.max() + p + 1, rgba.shape[1]), min(ys.max() + p + 1, rgba.shape[0]))
    img = Image.fromarray(rgba, "RGBA").crop(box).convert("RGBa")
    img = img.resize((width, round(width * img.height / img.width)), Image.LANCZOS).convert("RGBA")
    img.save(path, optimize=True)
    return img.size


def imageset(catalog, name, render):
    d = catalog / f"{name}.imageset"
    d.mkdir(parents=True, exist_ok=True)
    size = render(d / f"{name}.png")
    (d / "Contents.json").write_text(json.dumps({"images": [{"filename": f"{name}.png", "idiom": "universal"}], "info": INFO}, indent=2))
    print(f"{name}: {size[0]}x{size[1]}")


def logos(icon_path, catalog):
    im = np.asarray(Image.open(icon_path).convert("RGB")).astype(np.float32)
    red = im[..., 0]
    # Estimate the background from everything outside a rough white mask.
    bg_red = np.median(red[flood_from_border(im.min(axis=2) > 200)])
    white = np.clip((red - bg_red) / (255 - bg_red), 0, 1)  # 1 on white, 0 on blue
    solid = white > 0.5
    head = ~flood_from_border(solid)  # the head including its facial features
    features = head & ~solid
    edge = dilate(head, 5)  # keeps the antialiased outline
    alpha = np.where(features, 1.0, np.where(edge, white, 0.0))
    t = white[..., None]

    # White head with blue features, for blue surfaces.
    rgb = np.where(features[..., None], t * WHITE + (1 - t) * MEOW_BLUE, WHITE)
    imageset(catalog, "LogoWhite", lambda p: save_rgba(rgb, alpha, p, 512))
    # Blue head with white features, for light surfaces.
    rgb = np.where(features[..., None], t * MEOW_BLUE + (1 - t) * WHITE, MEOW_BLUE)
    imageset(catalog, "LogoBlue", lambda p: save_rgba(rgb, alpha, p, 512))


def mascot(path, catalog):
    im = np.asarray(Image.open(path).convert("RGBA")).astype(np.float32)
    a = im[..., 3]
    solid = a > 8
    # Keep only the region connected to the most central opaque pixel.
    ys, xs = np.nonzero(a > 250)
    cy, cx = np.array(a.shape) / 2
    i = np.argmin((ys - cy) ** 2 + (xs - cx) ** 2)
    lab = Image.fromarray(np.where(solid, 255, 0).astype(np.uint8)).copy()  # fromarray images are read-only
    ImageDraw.floodfill(lab, (int(xs[i]), int(ys[i])), 128)
    keep = dilate(np.asarray(lab) == 128, 3)
    removed = int((solid & ~keep).sum())
    print(f"Mascot: removed {removed} speck pixels")
    imageset(catalog, "Mascot", lambda p: save_rgba(im[..., :3], np.where(keep, a / 255, 0), p, 640, pad=0.02))


def app_icon(path, catalog):
    d = catalog / "AppIcon.appiconset"
    d.mkdir(parents=True, exist_ok=True)
    Image.open(path).convert("RGB").resize((1024, 1024), Image.LANCZOS).save(d / "AppIcon.png", optimize=True)
    (d / "Contents.json").write_text(json.dumps({
        "images": [{"filename": "AppIcon.png", "idiom": "universal", "platform": "ios", "size": "1024x1024"}],
        "info": INFO,
    }, indent=2))
    print("AppIcon: 1024x1024")


def color(r, g, b):
    return {"color-space": "srgb", "components": {"red": f"{r / 255:.3f}", "green": f"{g / 255:.3f}", "blue": f"{b / 255:.3f}", "alpha": "1.000"}}


def accent_color(catalog):
    d = catalog / "AccentColor.colorset"
    d.mkdir(parents=True, exist_ok=True)
    (d / "Contents.json").write_text(json.dumps({
        "colors": [
            {"idiom": "universal", "color": color(8, 121, 255)},
            {"idiom": "universal", "appearances": [{"appearance": "luminosity", "value": "dark"}], "color": color(61, 150, 255)},
        ],
        "info": INFO,
    }, indent=2))


def main():
    src, catalog = Path(sys.argv[1]), Path(sys.argv[2])
    catalog.mkdir(parents=True, exist_ok=True)
    (catalog / "Contents.json").write_text(json.dumps({"info": INFO}, indent=2))
    app_icon(src / "meow-logo-approved-blue-v2.png", catalog)
    logos(src / "meow-appicon-master-v1.png", catalog)
    mascot(src / "meow-mascot-wave-transparent-v1.png", catalog)
    accent_color(catalog)


if __name__ == "__main__":
    main()
