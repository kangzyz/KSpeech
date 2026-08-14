"""Rebuild every brand artifact in the repository from the art in imgs/.

Run it after replacing a source image:

    python scripts/make-brand-assets.py

Requires Pillow. It writes the Windows icons, the settings window brand mark,
the installer wizard images and the README banner; the EXE resource object
(rsrc_windows_amd64.syso) is produced separately, see Develop.md.

Icon sources must carry an alpha channel and no baked-in drop shadow: they are
scaled down to 16 px, where a keyed-out white background leaves a visible
fringe.
"""

from pathlib import Path

from PIL import Image

ROOT = Path(__file__).resolve().parent.parent
IMGS = ROOT / "imgs"

ICO_SIZES = [(16, 16), (20, 20), (24, 24), (32, 32), (40, 40), (48, 48), (64, 64), (128, 128), (256, 256)]

# Wails hands these PNGs straight to CreateIconFromResourceEx, which reads PNG
# but not a whole .ico file. The tray asset is 32 px because Windows asks for a
# 16 px icon at 100% DPI and a 32 px one at 200%, both exact halvings.
APP_PNG_SIZE = 256
TRAY_PNG_SIZE = 32

# Inno Setup 6.3/6.4 only read BMP here; PNG support arrived in 6.5.2. The
# wizard scales one image to the current DPI, so a 200% asset is supplied and
# scaled down rather than a 100% one scaled up.
WIZARD_IMAGE_SIZE = (328, 628)
WIZARD_SMALL_IMAGE_SIZE = (138, 140)
WIZARD_BACKGROUND = (255, 255, 255)

README_BANNER_WIDTH = 1600
# GitHub renders the social preview at 1280x640 and rejects files over 1 MB.
SOCIAL_PREVIEW_SIZE = (1280, 640)
MARK_SIZE = 128


def squared(image: Image.Image) -> Image.Image:
    """Trim the transparent border and centre the art on a square canvas."""
    trimmed = image.crop(image.getbbox())
    side = max(trimmed.size)
    canvas = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    canvas.alpha_composite(trimmed, ((side - trimmed.width) // 2, (side - trimmed.height) // 2))
    return canvas


def load(name: str) -> Image.Image:
    image = Image.open(IMGS / name)
    if image.mode not in ("RGBA", "LA"):
        raise SystemExit(f"{name} has no alpha channel; export the icon on a transparent background")
    return squared(image.convert("RGBA"))


def save(image: Image.Image, relative: str, **options) -> None:
    image.save(ROOT / relative, **options)
    print(relative)


def wizard_small_image(icon: Image.Image, destination: str) -> None:
    canvas = Image.new("RGB", WIZARD_SMALL_IMAGE_SIZE, WIZARD_BACKGROUND)
    scaled = icon.copy()
    scaled.thumbnail(WIZARD_SMALL_IMAGE_SIZE, Image.LANCZOS)
    canvas.paste(
        scaled,
        ((WIZARD_SMALL_IMAGE_SIZE[0] - scaled.width) // 2, (WIZARD_SMALL_IMAGE_SIZE[1] - scaled.height) // 2),
        scaled,
    )
    save(canvas, destination, format="BMP")


def main() -> None:
    app = load("icon-card-dark.png")
    tray = load("icon-card-dark-plain.png")

    save(app, "assets/kspeech-circle.ico", format="ICO", sizes=ICO_SIZES)
    save(tray, "assets/kspeech-tray.ico", format="ICO", sizes=ICO_SIZES)
    save(app.resize((APP_PNG_SIZE, APP_PNG_SIZE), Image.LANCZOS), "assets/kspeech-app.png", optimize=True)
    save(tray.resize((TRAY_PNG_SIZE, TRAY_PNG_SIZE), Image.LANCZOS), "assets/kspeech-tray.png", optimize=True)

    # The navy card holds up on both the dark and the light settings surfaces,
    # so the window shows exactly the same mark as the desktop icon.
    save(app.resize((MARK_SIZE, MARK_SIZE), Image.LANCZOS), "frontend/src/assets/brand-mark.png", optimize=True)

    banner = Image.open(IMGS / "installer-banner.png").convert("RGB")
    save(banner.resize(WIZARD_IMAGE_SIZE, Image.LANCZOS), "installer/wizard-image.bmp", format="BMP")
    wizard_small_image(app, "installer/wizard-small-image.bmp")

    # Both posters are flat vector art, so a 256 colour palette keeps them sharp
    # at roughly a third of the file size.
    source = Image.open(IMGS / "banner-source.png").convert("RGB")
    height = round(README_BANNER_WIDTH * source.height / source.width)
    readme = source.resize((README_BANNER_WIDTH, height), Image.LANCZOS)
    save(readme.convert("P", palette=Image.ADAPTIVE, colors=256), "imgs/banner.png", optimize=True)

    social = Image.open(IMGS / "social-preview-source.png").convert("RGB").resize(SOCIAL_PREVIEW_SIZE, Image.LANCZOS)
    save(social.convert("P", palette=Image.ADAPTIVE, colors=256), "imgs/social-preview.png", optimize=True)


if __name__ == "__main__":
    main()
