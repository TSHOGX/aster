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

- `/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc` (WenQuanYi Zen Hei)
- `/usr/share/fonts/truetype/wqy/wqy-microhei.ttc` (WenQuanYi Micro Hei)

## Procedure

matplotlib — set the font family before plotting:

```python
import matplotlib
matplotlib.rcParams["font.sans-serif"] = ["WenQuanYi Zen Hei", "WenQuanYi Micro Hei"]
matplotlib.rcParams["axes.unicode_minus"] = False  # keep the minus sign rendering
```

Pillow — load the TTC explicitly:

```python
from PIL import ImageFont
font = ImageFont.truetype("/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc", 24)
```

## Known Pitfalls

- Setting the font *after* creating the figure/axes may not take effect; set
  `rcParams` first.
- `axes.unicode_minus = False` avoids a missing-glyph box for negative-axis labels.

## Examples

A bar chart titled "电池健康度分布" renders with visible Chinese once
`font.sans-serif` includes a WenQuanYi family.
