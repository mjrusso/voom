#!/usr/bin/env python3

from pathlib import Path

from PIL import Image, ImageChops, ImageDraw


assets = Path(__file__).resolve().parent.parent / "assets"
artwork = Image.open(assets / "icon.png").convert("RGBA")
tile = artwork.resize((824, 824), Image.Resampling.LANCZOS)

mask = Image.new("L", (824 * 4, 824 * 4))
ImageDraw.Draw(mask).rounded_rectangle(
    (0, 0, mask.width - 1, mask.height - 1), radius=185 * 4, fill=255
)
mask = mask.resize(tile.size, Image.Resampling.LANCZOS)
tile.putalpha(ImageChops.multiply(tile.getchannel("A"), mask))

icon = Image.new("RGBA", (1024, 1024))
icon.alpha_composite(tile, (100, 100))
icon.save(assets / "voom.icns")
