package markitdown

// visionDriverScript is executed by the managed venv Python interpreter to run
// markitdown with LLM-assisted image captioning. The markitdown 0.1.4 CLI
// exposes no LLM flags — llm_client/llm_model/llm_prompt are constructor-only
// Python kwargs — so vision-assisted conversion must go through the library
// API instead of the plain CLI.
//
// The script is a fixed constant: connection parameters arrive exclusively
// via the MARKITDOWN_LLM_* environment variables set by visionEnv (keeping the
// API key out of argv), and the only positional argument is the file path.
//
// The embedded client is a minimal stdlib-only implementation of the OpenAI
// SDK surface markitdown consumes: chat.completions.create(model, messages)
// returning an object shaped like choices[0].message.content. The venv does
// not ship the `openai` package (markitdown[all] does not pull it in), so
// urllib.request is used directly; it validates TLS certificates by default
// and honors http_proxy/https_proxy environment variables, which the app sets
// globally when a proxy is configured (see core/proxy SetEnvVars).
//
// markitdown 0.1.4 captions images ONLY inside pptx decks (and standalone
// image files). Its PDF converter is pure pdfminer text extraction — embedded
// images are dropped entirely — and its docx/html converters truncate base64
// data URIs to inert stubs. The driver therefore adds two post-processing
// passes so the vision model actually SEES document images:
//
//   - PDF pass (pdfminer + Pillow, both venv-resident): walks page
//     XObjects, extracts embedded images (DCTDecode/JPXDecode pass-through,
//     FlateDecode raw-sample reconstruction for DeviceGray/DeviceRGB/
//     DeviceCMYK at 8bpc, and Flate-wrapped DCT/JPX chains; array-form
//     color spaces — ICCBased/CalGray/CalRGB — are skipped), and appends an
//     "## Embedded images" section with one LLM caption per unique image
//     (deduplicated by content hash). Every decoded path is BOUNDED —
//     pdfminer's get_data() is never used, because it materializes the full
//     filter-chain expansion (a crafted FlateDecode/LZW stream declaring
//     tiny dimensions can expand gigabytes); FlateDecode streams are
//     decompressed through a cap derived from the declared dimensions or
//     the physical stream size, other filter chains (LZW, RunLength,
//     predictors, encrypted documents) are skipped as unsupported, and the
//     child's address space is capped via RLIMIT_AS (POSIX) as a catch-all
//     so any remaining bomb degrades to a caption-less image instead of an
//     OOM.
//   - data-URI pass: the conversion runs with keep_data_uris=True so
//     docx/html/epub embedded images survive as markdown data-URI images;
//     each base64 blob is replaced in place by
//     ![caption](embedded-image-N) — the model gets the description AND the
//     context is spared megabytes of base64. Images that already carry alt
//     text (markitdown's own pptx captioning, or a docx/html alt attribute)
//     keep that text and are NOT re-captioned, so a pptx picture costs one
//     LLM round-trip, not two. Every data:image/* mime type is stripped,
//     not just the raster types Pillow can decode — an EMF or SVG payload
//     is replaced with a neutral label rather than leaked verbatim.
//
// Cost and abuse bounds, shared across markitdown's internal captioning and
// both passes: every unique image (deduplicated by content hash) is captioned
// — there is deliberately no per-document call cap — images smaller than 32px
// in either dimension are ignored as decorations, and images are normalized
// (RGB JPEG, longest side ≤ 2048px) before upload. The outer bound is the
// Go-side per-file vision deadline shared with the plain-CLI fallback (see
// Converter.convert).
//
// Failure semantics: every pass is individually exception-guarded and every
// per-image captioning error is caught, so a broken vision endpoint (or an
// unsupported image encoding) yields a converted document without captions
// rather than a failed conversion. Only the base markitdown conversion can
// fail the driver as a whole — and that falls back to the plain CLI on the
// Go side (see Converter.convert). Diagnostic warnings go to stderr, which
// the Go side logs at debug level without polluting the document body.
const visionDriverScript = `import base64, hashlib, io, json, os, re, sys, urllib.request, zlib

# Bound the child's address space so a decompression bomb (a crafted PDF
# FlateDecode/LZW stream declaring tiny dimensions, a hostile archive)
# becomes a catchable MemoryError — handled per image/pass as a caption-less
# skip — instead of an OOM kill or a machine-freezing multi-gigabyte
# transient allocation. Generous enough for legitimate conversions (document
# parsing plus several 40-megapixel image buffers); POSIX only, best-effort.
try:
    import resource
    _addr_space_cap = 2 * 1024 * 1024 * 1024
    resource.setrlimit(resource.RLIMIT_AS, (_addr_space_cap, _addr_space_cap))
except Exception:
    pass

_api_key = os.environ["MARKITDOWN_LLM_API_KEY"]
_base_url = os.environ["MARKITDOWN_LLM_BASE_URL"].rstrip("/")
_model = os.environ["MARKITDOWN_LLM_MODEL"]
_raw_prompt = os.environ.get("MARKITDOWN_LLM_PROMPT")
_caption_prompt = _raw_prompt if _raw_prompt else "Write a detailed caption for this image."

_MIN_DIM = 32
_MAX_DIM = 2048
_MAX_PIXELS = 40000000


class _Obj:
    """Minimal attribute bag mirroring the OpenAI SDK response shape."""


class _Completions:
    def create(self, model=None, messages=None):
        payload = json.dumps({"model": model, "messages": messages}).encode("utf-8")
        req = urllib.request.Request(
            _base_url + "/chat/completions",
            data=payload,
            method="POST",
            headers={
                "Content-Type": "application/json",
                "Authorization": "Bearer " + _api_key,
            },
        )
        with urllib.request.urlopen(req, timeout=600) as resp:
            data = json.loads(resp.read().decode("utf-8"))
        # Pass content through verbatim (including None): markitdown treats a
        # None caption as "no description" and skips it.
        msg = _Obj()
        msg.content = data["choices"][0]["message"]["content"]
        choice = _Obj()
        choice.message = msg
        result = _Obj()
        result.choices = [choice]
        return result


class _Chat:
    def __init__(self):
        self.completions = _Completions()


class _Client:
    def __init__(self):
        self.chat = _Chat()


_client = _Client()


def _caption(jpeg_bytes):
    data_uri = "data:image/jpeg;base64," + base64.b64encode(jpeg_bytes).decode("ascii")
    messages = [
        {
            "role": "user",
            "content": [
                {"type": "text", "text": _caption_prompt},
                {"type": "image_url", "image_url": {"url": data_uri}},
            ],
        }
    ]
    content = _client.chat.completions.create(model=_model, messages=messages).choices[0].message.content
    if isinstance(content, str):
        content = content.strip()
    return content or None


def _to_jpeg(img):
    # Normalize any decoded image to a bounded RGB/grayscale JPEG so caption
    # payloads stay small regardless of the source encoding.
    if img.mode in ("RGBA", "LA", "PA", "P"):
        img = img.convert("RGBA")
        bg = Image.new("RGB", img.size, (255, 255, 255))
        bg.paste(img, mask=img.split()[-1])
        img = bg
    elif img.mode not in ("RGB", "L"):
        img = img.convert("RGB")
    if max(img.size) > _MAX_DIM:
        img.thumbnail((_MAX_DIM, _MAX_DIM))
    buf = io.BytesIO()
    img.save(buf, "JPEG", quality=85)
    return buf.getvalue()


_captions = {}


def _caption_cached(jpeg, size, label):
    if size[0] < _MIN_DIM or size[1] < _MIN_DIM:
        return None
    key = hashlib.sha256(jpeg).hexdigest()
    if key in _captions:
        return _captions[key]
    try:
        cap = _caption(jpeg)
    except Exception as e:
        print("markitdown driver: caption failed for %s: %s" % (label, e), file=sys.stderr)
        return None
    _captions[key] = cap
    return cap


_DATA_URI_RE = re.compile(
    r"!\[([^\]]*)\]\(data:image/([a-z0-9.+-]+);base64,([A-Za-z0-9+/=\s]+)\)",
    re.IGNORECASE,
)


def _datauri_pass(markdown):
    counter = [0]

    def repl(m):
        counter[0] += 1
        label = "embedded image %d" % counter[0]
        existing = m.group(1).replace("\n", " ").strip()
        cap = existing or None
        # Reuse an existing alt text (markitdown's own pptx captioning, or an
        # HTML/docx alt attribute) instead of paying a second LLM round-trip
        # for the same image. Only caption when there is nothing to reuse.
        if not cap:
            try:
                raw = base64.b64decode(re.sub(r"\s+", "", m.group(3)))
                img = Image.open(io.BytesIO(raw))
                img.load()
                cap = _caption_cached(_to_jpeg(img), img.size, label)
            except Exception as e:
                print("markitdown driver: caption failed for %s: %s" % (label, e), file=sys.stderr)
        # The base64 blob is replaced by the caption (or a neutral label)
        # even when captioning failed, so raw image payloads never reach
        # the model context.
        alt = (cap or m.group(1) or label).replace("\n", " ").strip() or label
        return "![%s](%s)" % (alt, label.replace(" ", "-"))

    return _DATA_URI_RE.sub(repl, markdown)


def _pdf_color_mode(cs):
    cs = resolve1(cs)
    name = getattr(cs, "name", None)
    if name in ("DeviceGray", "CalGray"):
        return "L"
    if name in ("DeviceRGB", "CalRGB"):
        return "RGB"
    if name == "DeviceCMYK":
        return "CMYK"
    if name == "ICCBased":
        try:
            n = int(resolve1(cs.attrs.get("N")))
        except Exception:
            return None
        return {1: "L", 3: "RGB", 4: "CMYK"}.get(n)
    return None


def _flate_limited(raw, limit):
    # Decompress at most 'limit' bytes: decompressobj.decompress(raw,
    # max_length) stops at the cap without ever allocating beyond it, so a
    # crafted stream that would expand past the limit (zlib ratio ~1000:1)
    # is detected, not materialized. None = over-limit, truncated or corrupt.
    try:
        d = zlib.decompressobj()
        out = d.decompress(raw, limit)
        if d.unconsumed_tail or not d.eof:
            return None
        return out
    except Exception:
        return None


def _pdf_image_from_stream(stream):
    attrs = stream.attrs
    try:
        w = int(resolve1(attrs.get("Width")))
        h = int(resolve1(attrs.get("Height")))
    except Exception:
        return None
    if w < _MIN_DIM or h < _MIN_DIM or w * h > _MAX_PIXELS:
        return None
    # NEVER call stream.get_data(): it materializes the WHOLE filter-chain
    # expansion (zlib/LZW bombs expand gigabytes regardless of the declared
    # dimensions) before any length check can run — and the RLIMIT_AS
    # catch-all is best-effort (unsupported on macOS). Only the encodings we
    # can decode BOUNDED are supported; everything else is skipped.
    raw = getattr(stream, "rawdata", None)
    if raw is None or getattr(stream, "decipher", None) is not None:
        # No physical bytes, or an encrypted document: skip rather than
        # fall back to an unbounded decode.
        return None
    try:
        fl = list(stream.get_filters())
    except Exception:
        return None
    names = [getattr(f, "name", None) for (f, _p) in fl]
    if any(p for (_f, p) in fl):
        # DecodeParms (predictors etc.) could rewrite the byte layout —
        # unsupported on the bounded paths.
        return None
    if names in (["DCTDecode"], ["JPXDecode"]):
        # The stored bytes ARE the encoded JPEG/JP2 payload — no expansion
        # risk; Pillow's own decompression-bomb guard plus the declared-
        # dimension check above bound the decode.
        try:
            img = Image.open(io.BytesIO(raw))
            img.load()
            return img
        except Exception:
            return None
    if names == ["FlateDecode"]:
        # Decoded raw samples: reconstruct by colorspace. Indexed, separated
        # and masked colorspaces are skipped (unsupported, harmless).
        mode = _pdf_color_mode(attrs.get("ColorSpace"))
        try:
            bpc = int(resolve1(attrs.get("BitsPerComponent")) or 8)
        except Exception:
            bpc = 8
        if mode is None or bpc != 8:
            return None
        comps = {"L": 1, "RGB": 3, "CMYK": 4}[mode]
        need = w * h * comps
        data = _flate_limited(raw, need + 65536)
        if data is None or len(data) < need:
            return None
        try:
            return Image.frombytes(mode, (w, h), data[:need])
        except Exception:
            return None
    if len(names) == 2 and names[0] == "FlateDecode" and names[1] in ("DCTDecode", "JPXDecode"):
        # Flate-wrapped encoded image: bound the expansion by the physical
        # compressed size (flate over a JPEG payload is ~1:1; generous
        # slack), then hand the encoded bytes to Pillow.
        data = _flate_limited(raw, len(raw) * 8 + (1 << 20))
        if data is None:
            return None
        try:
            img = Image.open(io.BytesIO(data))
            img.load()
            return img
        except Exception:
            return None
    # Any other filter chain (LZWDecode, RunLengthDecode, ASCII-armored,
    # …): unsupported — decoding them would require the unbounded path.
    return None


def _pdf_pass(path, markdown):
    seen = set()
    entries = []
    with open(path, "rb") as f:
        doc = PDFDocument(PDFParser(f))
        for pageno, page in enumerate(PDFPage.create_pages(doc), 1):
            try:
                xobjs = dict_value(resolve1(page.resources.get("XObject")) or {})
            except Exception:
                continue
            for name, ref in xobjs.items():
                try:
                    stream = resolve1(ref)
                    attrs = getattr(stream, "attrs", None)
                    if not attrs:
                        continue
                    subtype = resolve1(attrs.get("Subtype"))
                    if getattr(subtype, "name", None) != "Image":
                        continue
                    img = _pdf_image_from_stream(stream)
                    if img is None:
                        continue
                    jpeg = _to_jpeg(img)
                    key = hashlib.sha256(jpeg).hexdigest()
                    if key in seen:
                        continue
                    seen.add(key)
                    cap = _caption_cached(jpeg, img.size, "pdf image on page %d" % pageno)
                    entries.append((pageno, img.width, img.height, cap))
                except Exception as e:
                    print("markitdown driver: pdf image pass error (page %d): %s" % (pageno, e), file=sys.stderr)
    if not entries:
        return markdown
    lines = ["", "", "## Embedded images", "", "Images extracted from this PDF and described by an AI vision model:", ""]
    for pageno, w, h, cap in entries:
        if cap:
            lines.append("- Page %d, %dx%d: %s" % (pageno, w, h, cap.replace("\n", " ")))
        else:
            lines.append("- Page %d, %dx%d: (image present; no description available)" % (pageno, w, h))
    return markdown + "\n".join(lines) + "\n"


from PIL import Image
from pdfminer.pdfparser import PDFParser
from pdfminer.pdfdocument import PDFDocument
from pdfminer.pdfpage import PDFPage
from pdfminer.pdftypes import resolve1, dict_value

from markitdown import MarkItDown

_md = MarkItDown(llm_client=_client, llm_model=_model, llm_prompt=_raw_prompt)
_markdown = _md.convert(sys.argv[1], keep_data_uris=True).markdown

try:
    _markdown = _datauri_pass(_markdown)
except Exception as e:
    print("markitdown driver: data-uri pass failed: %s" % e, file=sys.stderr)
    # Safety net: never leak full base64 payloads if the pass failed wholesale.
    _markdown = re.sub(r"data:image/([a-z0-9.+-]+);base64,[A-Za-z0-9+/=\s]+", r"data:image/\1;base64...", _markdown)

if sys.argv[1].lower().endswith(".pdf"):
    try:
        _markdown = _pdf_pass(sys.argv[1], _markdown)
    except Exception as e:
        print("markitdown driver: pdf pass failed: %s" % e, file=sys.stderr)

sys.stdout.write(_markdown)
`

// visionEnvName* are the environment variables the driver script reads.
// Namespaced with the MARKITDOWN_ prefix to avoid colliding with generic
// OPENAI_API_KEY semantics the child might otherwise inherit.
const (
	visionEnvAPIKey  = "MARKITDOWN_LLM_API_KEY"
	visionEnvBaseURL = "MARKITDOWN_LLM_BASE_URL"
	visionEnvModel   = "MARKITDOWN_LLM_MODEL"
	visionEnvPrompt  = "MARKITDOWN_LLM_PROMPT"
)
