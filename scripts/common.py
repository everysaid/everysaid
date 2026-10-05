"""What the scripts share: external tools, pictures, the torch device, the immich API and the local
review pages. Imported by the scripts from their own folder (`import common`).
"""
import hmac
import http.cookies
import json
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
from chronika import config, media  # noqa: E402

link_or_copy = media.link_or_copy       # a hard link, or a copy across file systems

VIDEO = (".mp4", ".mov", ".3gp", ".webm", ".m4v")
MEDIA = os.path.join(config.MEDIA_STORE, "media") + os.sep     # the archive's media, until the photo library
TOOLS = {"exiftool": "ExifTool, https://exiftool.org", "ffmpeg": "FFmpeg", "ffprobe": "FFmpeg",
         "idevicebackup2": "libimobiledevice", "idevice_id": "libimobiledevice",
         "adb": "Android platform-tools"}


def tool(name):
    """The full path of an external program, or the way out saying which package provides it.
    On Windows `which` also tries PATHEXT (exiftool.exe)."""
    path = shutil.which(name)
    if not path:
        sys.exit(f"Χρειάζεται το {name} ({TOOLS.get(name, name)}) και δεν βρέθηκε στο PATH.")
    return path


def run(cmd, **kw):
    """subprocess.run with the program looked up first; the caller checks returncode."""
    return subprocess.run([tool(cmd[0]), *cmd[1:]], **kw)


# --- files ----------------------------------------------------------------------------------------

def media_file(rel):
    """The file of an archive `media.path` (stored with '/', whatever the system)."""
    return os.path.join(config.CACHE, *rel.split("/"))


def in_media(path):
    return os.path.normpath(path).startswith(MEDIA)


def flat(path):
    """A path as one file name, for a cache of thumbnails."""
    return path.replace("/", "__").replace("\\", "__").replace(":", "_")


# --- pictures -------------------------------------------------------------------------------------

def _pil():
    from PIL import Image, ImageOps
    try:
        import pillow_heif
        pillow_heif.register_heif_opener()      # HEIC/HEIF, as iPhones take them
    except ImportError:
        pass
    return Image, ImageOps


def frame(path, seeks=("1", "0")):
    """A video's frame (a second in, else the first) as a Pillow image, or None."""
    Image, _ = _pil()
    with tempfile.TemporaryDirectory() as tmp:
        out = os.path.join(tmp, "frame.jpg")
        for seek in seeks:
            r = run(["ffmpeg", "-v", "error", "-y", "-ss", seek, "-i", path, "-frames:v", "1", out],
                    capture_output=True)
            if r.returncode == 0 and os.path.exists(out):
                with Image.open(out) as im:
                    return im.convert("RGB")
    return None


def picture(path, side=None):
    """A picture (its first frame), turned as its EXIF says, in RGB, at most side×side; for a
    video, its frame a second in. None when it cannot be read."""
    if path.lower().endswith(VIDEO):
        im = frame(path)
    else:
        Image, ImageOps = _pil()
        try:
            with Image.open(path) as src:
                src.seek(0)
                if side:
                    src.draft("RGB", (side, side))      # JPEG: decoded at a smaller scale, faster
                im = ImageOps.exif_transpose(src).convert("RGB")
        except (OSError, ValueError, Image.DecompressionBombError):
            return None
    if im is not None and side:
        im.thumbnail((side, side))
    return im


def jpeg(path, out, side, quality=85):
    """A JPEG of path at most side×side in out; whether it was made."""
    im = picture(path, side)
    if im is None:
        return False
    im.save(out, "JPEG", quality=quality)
    return True


def dimensions(path):
    """(width, height) as stored (not turned), (0, 0) when it cannot be read."""
    if path.lower().endswith(VIDEO):
        r = run(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height",
                 "-of", "csv=p=0:s=x", path], capture_output=True, text=True, encoding="utf-8")
        try:
            w, h = r.stdout.strip().split("x")[:2]
            return int(w), int(h)
        except ValueError:
            return 0, 0
    Image, _ = _pil()
    try:
        with Image.open(path) as im:
            return im.size
    except (OSError, ValueError):
        return 0, 0


def exiftool_json(args, paths, chunk=500):
    """exiftool -j over many files, in chunks; the records it returns."""
    out = []
    for i in range(0, len(paths), chunk):
        r = run(["exiftool", "-q", "-j", *args, *paths[i:i + chunk]], capture_output=True, text=True, encoding="utf-8")
        if r.returncode not in (0, 1):  # 1: some files had nothing to say, or could not be read
            sys.exit(f"exiftool: {r.stderr.strip()[:500]}")
        out += json.loads(r.stdout or "[]")
    return out


# --- local models ---------------------------------------------------------------------------------

def device():
    """Where torch runs: a CUDA (or ROCm) GPU, Apple's MPS, else the CPU."""
    import torch
    if torch.cuda.is_available():
        return "cuda"
    if getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
        return "mps"
    return "cpu"


def features(out):
    """The embedding from get_image_features / get_text_features: a tensor in transformers 4, the
    pooled output in 5."""
    return out if hasattr(out, "norm") else out.pooler_output


# --- immich ---------------------------------------------------------------------------------------

def immich(method, path, body=None, raw=False, headers=None):
    """A call to the immich API (config `[immich] url`, key `immich-key`). JSON in and out, or the
    bytes with raw."""
    url = config.require(config.IMMICH_URL, "immich", "url") + "/api" + path
    key = config.secret("immich-key") or sys.exit("Δεν βρέθηκε το κλειδί του immich (immich-key).")
    data = json.dumps(body).encode() if isinstance(body, (dict, list)) else body
    hdrs = {"x-api-key": key, "Accept": "application/json" if not raw else "*/*", **(headers or {})}
    if isinstance(body, (dict, list)):
        hdrs["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
    with urllib.request.urlopen(req, timeout=300) as r:
        return r.read() if raw else json.load(r)


# --- local review pages ---------------------------------------------------------------------------

TOKEN = secrets.token_urlsafe(24)      # per run: the pages send it with every change
# Put in each page before its own script: every fetch() then carries the token.
TOKEN_SCRIPT = ("<script>{const t = '%s', f = window.fetch; window.fetch = (u, o = {}) => "
                "f(u, {...o, headers: {...(o.headers || {}), 'X-Token': t}});}</script>" % TOKEN)
KEY = secrets.token_urlsafe(24)        # per run: in the address printed on the terminal, then a cookie
# Only the page's own scripts (with the nonce) run: markup that came in with the data cannot.
CSP = ("default-src 'self'; script-src 'nonce-%s'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; "
       "media-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")


def address(port):
    """The page's address with the run's key, printed on the terminal: the only way in."""
    return f"http://127.0.0.1:{port}/?k={KEY}"


def page(html):
    """A page template as sent: the token's script before its own, every script with a fresh nonce.
    Returns it with the Content-Security-Policy header's value. Applied before the data goes in."""
    nonce = secrets.token_urlsafe(16)
    html = html.replace("<script>", TOKEN_SCRIPT + "<script>", 1).replace("<script>", f'<script nonce="{nonce}">')
    return html, CSP % nonce


def _cookie(handler):
    return f"chronika-{handler.server.server_address[1]}"      # one per port: two pages do not share it


def _host(handler):
    """Host (and Origin, if sent) name this server on 127.0.0.1 or localhost (no DNS rebinding)."""
    port = handler.server.server_address[1]
    hosts = {f"127.0.0.1:{port}", f"localhost:{port}"}
    if handler.headers.get("Host") not in hosts:
        return False
    origin = handler.headers.get("Origin")
    return not origin or origin.removeprefix("http://") in hosts


def login(handler):
    """The visit by the printed address: the key becomes a cookie (HttpOnly, SameSite=Strict) and the
    browser goes on to the page without it in the address. Whether it answered the request."""
    url = urllib.parse.urlparse(handler.path)
    key = urllib.parse.parse_qs(url.query).get("k", [""])[0]
    if handler.command != "GET" or url.path != "/" or not key or not _host(handler) \
            or not hmac.compare_digest(key, KEY):
        return False
    handler.send_response(303)
    handler.send_header("Set-Cookie", f"{_cookie(handler)}={KEY}; HttpOnly; SameSite=Strict; Path=/")
    handler.send_header("Location", "/")
    handler.send_header("Content-Length", "0")
    handler.end_headers()
    return True


def allowed(handler):
    """Whether a request to a local page may be served: addressed to this server (_host), with the
    run's cookie (set by login(); another user of the machine or another site has not got it), and,
    for anything but GET, the run's token too, which another site's page cannot know."""
    if not _host(handler):
        return False
    jar = http.cookies.SimpleCookie()
    try:
        jar.load(handler.headers.get("Cookie", ""))
    except http.cookies.CookieError:
        return False
    got = jar.get(_cookie(handler))
    if not got or not hmac.compare_digest(got.value, KEY):
        return False
    return handler.command in ("GET", "HEAD") or hmac.compare_digest(handler.headers.get("X-Token", ""), TOKEN)
