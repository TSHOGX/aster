---
status: in-use
support_count: 999
confidence: 1.0
---
# Render Chinese text in charts (matplotlib / Pillow)

## When To Use

Any time you draw a chart, plot, or image that contains Chinese characters. The
default matplotlib/Pillow font renders Chinese as blank "tofu" boxes (□□□).

## Context

Two WenQuanYi CJK fonts are installed in the image:

- `/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc` (WenQuanYi Zen Hei) — bolder,
  better for titles and labels
- `/usr/share/fonts/truetype/wqy/wqy-microhei.ttc` (WenQuanYi Micro Hei) — lighter

The image's `matplotlibrc` already sets a CJK-capable default
(`font.family: sans-serif, WenQuanYi Zen Hei`), so **plain charts with Chinese
titles/labels render correctly with no configuration at all.** You only need this
skill when you override fonts yourself, or when Chinese still comes out as tofu.

## Procedure

Usually: just plot. Add nothing. Only set `axes.unicode_minus` if a negative axis
label shows a tofu box:

```python
import matplotlib
matplotlib.rcParams["axes.unicode_minus"] = False
```

If you do override the font family, keep a CJK family **inside the `font.family`
list**:

```python
import matplotlib
matplotlib.rcParams["font.family"] = ["sans-serif", "WenQuanYi Zen Hei"]
# monospace tables/code with Chinese:
matplotlib.rcParams["font.family"] = ["monospace", "WenQuanYi Zen Hei"]
```

Pillow — load the TTC explicitly:

```python
from PIL import ImageFont
font = ImageFont.truetype("/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc", 24)
```

## Known Pitfalls

- **`rcParams["font.family"] = "monospace"` (a bare generic) reintroduces tofu.**
  matplotlib's per-glyph fallback only walks the `font.family` list; it does *not*
  consult the `font.sans-serif` sublist. Always append a CJK family to the list.
- Setting `font.sans-serif` alone is not a reliable fix, for the same reason — it
  only applies when `font.family` resolves to `sans-serif`.
- Set `rcParams` before creating the figure/axes; afterwards may not take effect.
- Pillow does not use fontconfig — pass the `.ttc` path explicitly.
- The WenQuanYi `... Mono` sub-faces aren't registered with matplotlib, so CJK in a
  monospace chart won't align perfectly with ASCII. Fine for labels; for strict
  column alignment render via a fontconfig-based tool (Cairo, WeasyPrint), where
  the generic `monospace` falls back to `WenQuanYi Zen Hei Mono`.
- If you hit `findfont: Font family ... not found`, the font cache is stale:
  `rm -rf "$MPLCONFIGDIR"` (or `/tmp/matplotlib`) and retry.
